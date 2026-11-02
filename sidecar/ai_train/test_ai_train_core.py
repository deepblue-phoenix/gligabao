"""Local unit tests for the provider-neutral training code. No GPU, Modal or ML libs needed.

    cd sidecar/ai_train && python -m unittest test_ai_train_core -v
"""
import base64
import hashlib
import json
import os
import sys
import tempfile
import threading
import time
import unittest
from datetime import datetime
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from unittest import mock

import training_data
from ai_train_core import agent_run, dataset, event_log, paths, progress_events, trained_model
from providers.base import CallStatus
from ai_train_core import model_families, training_common
from ai_train_core.gemma4 import prompt_format
from ai_train_core.gemma4.model_config import MODEL_CONFIGS, get_model_config
from ai_train_core.gemma4.prompt_format import (bos_prefix, extract_model_response, format_inference_prompt,
                                         format_instruction, stop_token_ids)

REQUIRED_KEYS = ["model_name", "display_name", "total_params", "trainable_params_r16",
                 "trainable_percent_r16", "recommended_gpu", "default_gpu", "min_gpu_memory",
                 "recommended_batch_size", "recommended_grad_accumulation"]


class ModelConfigTest(unittest.TestCase):
    def test_required_keys_and_effective_batch(self):
        for size, cfg in MODEL_CONFIGS.items():
            for key in REQUIRED_KEYS:
                self.assertIn(key, cfg, f"{size} missing {key}")
            self.assertEqual(cfg["recommended_batch_size"] * cfg["recommended_grad_accumulation"], 8)

    def test_invalid_size(self):
        with self.assertRaises(ValueError):
            get_model_config("invalid")


class ModelFamiliesTest(unittest.TestCase):
    """Every registered family provides the modules and names the family-neutral code relies on."""

    def test_default_is_registered(self):
        self.assertIn(model_families.DEFAULT_MODEL_FAMILY, model_families.MODEL_FAMILIES)

    def test_contract(self):
        needs = {"model_config": ("MODEL_CONFIGS", "MODEL_SIZES", "get_model_config"),
                 "prompt_format": ("format_instruction", "dataset_formatting_func", "format_inference_prompt", "bos_prefix",
                                   "stop_token_ids", "extract_model_response")}
        for family in model_families.MODEL_FAMILIES:
            for module, names in needs.items():
                m = model_families.family_module(module, family)
                for name in names:
                    self.assertTrue(hasattr(m, name), f"{family}.{module} lacks {name}")

    def test_lookups(self):
        self.assertEqual(model_families.model_sizes(), tuple(MODEL_CONFIGS))
        self.assertEqual(model_families.get_model_config("4b"), MODEL_CONFIGS["4b"])

    def test_unknown_family(self):
        with self.assertRaises(ValueError):
            model_families.family_module("model_config", "nope")


class FakeTokenizer:
    """Just enough of a HF tokenizer for prompt_format: a vocab of special tokens, optional automatic BOS."""
    unk_token_id = 3

    def __init__(self, vocab=None, adds_bos=False, bos_token="<bos>"):
        self.vocab = {"<eos>": 1, "<bos>": 2, "<|turn>": 105, "<turn|>": 106} if vocab is None else vocab
        self.adds_bos, self.bos_token = adds_bos, bos_token
        self.bos_token_id = self.vocab.get(bos_token) if bos_token else None

    def convert_tokens_to_ids(self, token):
        return self.vocab.get(token, self.unk_token_id)

    def __call__(self, text):
        return {"input_ids": ([self.bos_token_id] if self.adds_bos else []) + [10]}


class PromptFormatTest(unittest.TestCase):
    def test_with_and_without_input(self):
        self.assertEqual(
            format_instruction({"instruction": "A", "input": "", "output": "B"})["text"],
            "<|turn>user\nA<turn|>\n<|turn>model\nB<turn|>")
        self.assertEqual(
            format_instruction({"instruction": "A", "input": "C", "output": "B"})["text"],
            "<|turn>user\nA\n\nC<turn|>\n<|turn>model\nB<turn|>")

    def test_training_text_ends_with_end_of_turn(self):
        """The model learns to stop only if the example ends with Gemma 4's real end-of-turn token."""
        for ex in ({"instruction": "A", "output": "B"}, {"instruction": "A", "input": "C", "output": "B"}):
            text = format_instruction(ex)["text"]
            self.assertTrue(text.endswith("B<turn|>"))
            self.assertEqual(text.count("<|turn>"), 2)
            self.assertEqual(text.count("<turn|>"), 2)

    def test_no_gemma3_markers(self):
        """<start_of_turn>/<end_of_turn> are not in Gemma 4's vocabulary: they would be learned as plain text."""
        text = format_instruction({"instruction": "A", "input": "C", "output": "B"})["text"]
        for old in ("<start_of_turn>", "<end_of_turn>"):
            self.assertNotIn(old, text)
            self.assertNotIn(old, format_inference_prompt("A"))
            self.assertNotIn(old, prompt_format.MODEL_TURN_MARKER)

    def test_inference_prompt_matches_training_prefix(self):
        text = format_instruction({"instruction": "A", "input": "", "output": "B"})["text"]
        self.assertTrue(text.startswith(format_inference_prompt("A")))
        self.assertTrue(format_inference_prompt("A").endswith("<|turn>model\n"))

    def test_extract_response(self):
        self.assertEqual(extract_model_response("user\nA\n<|turn>model\n hi "), "hi")
        self.assertEqual(extract_model_response(" hi "), "hi")
        self.assertEqual(extract_model_response("hi<turn|>"), "hi")
        self.assertEqual(extract_model_response("<|turn>model\nhi <turn|><eos>"), "hi")

    def test_bos_prefix(self):
        self.assertEqual(bos_prefix(FakeTokenizer(adds_bos=False)), "<bos>")   # tokenizer does not add it: we do
        self.assertEqual(bos_prefix(FakeTokenizer(adds_bos=True)), "")         # it does: never twice
        self.assertEqual(bos_prefix(FakeTokenizer(bos_token=None)), "")        # no BOS token at all

    def test_stop_token_ids(self):
        self.assertEqual(stop_token_ids(FakeTokenizer()), [106, 1])
        # a token missing from the vocabulary (Gemma 3's marker, say) must never become a stop id
        self.assertEqual(stop_token_ids(FakeTokenizer(vocab={"<eos>": 1})), [1])
        self.assertEqual(stop_token_ids(FakeTokenizer(vocab={})), [])

    def test_add_text_prefix(self):
        class FakeDataset:
            def __init__(self, rows):
                self.rows = rows

            def map(self, fn):
                return FakeDataset([{**r, **fn(r)} for r in self.rows])

        ds = FakeDataset([{"text": "a"}, {"text": "b"}])
        self.assertIs(training_common.add_text_prefix(ds, ""), ds)
        self.assertEqual([r["text"] for r in training_common.add_text_prefix(ds, "<bos>").rows], ["<bos>a", "<bos>b"])


class Gemma4TokenizerTest(unittest.TestCase):
    """Against the real Gemma 4 tokenizer (Hugging Face Hub). Skipped when it cannot be loaded (offline, `tokenizers`
    missing); this is the check that the training text really ends with the id generation stops on."""
    END_OF_TURN_ID, START_OF_TURN_ID, EOS_ID = 106, 105, 1

    @classmethod
    def setUpClass(cls):
        try:
            from tokenizers import Tokenizer
            cls.tok = Tokenizer.from_pretrained("google/gemma-4-E4B-it")
        except Exception as e:
            raise unittest.SkipTest(f"Gemma 4 tokenizer not available: {e!r}")

    def test_markers_are_single_special_tokens(self):
        self.assertEqual(self.tok.encode("<turn|>", add_special_tokens=False).ids, [self.END_OF_TURN_ID])
        self.assertEqual(self.tok.encode("<|turn>", add_special_tokens=False).ids, [self.START_OF_TURN_ID])

    def test_gemma3_markers_are_not_special_tokens(self):
        for old in ("<start_of_turn>", "<end_of_turn>"):
            self.assertGreater(len(self.tok.encode(old, add_special_tokens=False).ids), 1, old)

    def test_training_text_ends_with_the_stop_token_id(self):
        text = format_instruction({"instruction": "Where is Shell headquartered?", "output": "In London."})["text"]
        ids = self.tok.encode(text, add_special_tokens=False).ids
        self.assertEqual(ids[-1], self.END_OF_TURN_ID)
        self.assertEqual(ids.count(self.START_OF_TURN_ID), 2)

    def test_inference_prompt_ends_with_open_model_turn(self):
        ids = self.tok.encode(format_inference_prompt("hi"), add_special_tokens=False).ids
        self.assertEqual(ids[-3], self.START_OF_TURN_ID)   # <|turn> "model" "\n"
        self.assertEqual(self.tok.decode([ids[-2]]), "model")


class DatasetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.data = str(Path(self.tmp.name) / "d.jsonl")
        self.addCleanup(self.tmp.cleanup)

    def test_sync_only_when_changed(self):
        commits = []
        content = b'{"instruction":"a","input":"","output":"b"}\n'
        kw = dict(commit_fn=commits.append, data_dir=self.tmp.name)
        self.assertTrue(dataset.sync_dataset(content, "d.jsonl", **kw)["uploaded"])
        self.assertFalse(dataset.sync_dataset(content, "d.jsonl", **kw)["uploaded"])
        self.assertTrue(dataset.sync_dataset(content + content, "d.jsonl", **kw)["uploaded"])
        self.assertEqual((Path(self.tmp.name) / "d.jsonl").read_bytes(), content + content)
        self.assertEqual(commits, [[self.tmp.name]] * 2)

    def test_validate(self):
        Path(self.data).write_text('{"instruction":"a","input":"","output":"bb"}\n')
        self.assertEqual(dataset.validate_dataset(self.data)["num_examples"], 1)
        Path(self.data).write_text('{"instruction":"a"}\n')
        with self.assertRaises(ValueError):
            dataset.validate_dataset(self.data)

    def test_bundled_datasets_are_valid(self):
        """Every dataset of build/datasets_training whose data file has been generated is valid."""
        found, _ = training_data.list_local_datasets()
        for ds in found:
            self.assertGreater(dataset.validate_dataset(str(ds.path))["num_examples"], 0, ds.name)


class DatasetNameTest(unittest.TestCase):
    def test_resolve(self):
        self.assertEqual(dataset.resolve_dataset_path("my-set_1.jsonl"), f"{paths.DATA_DIR}/my-set_1.jsonl")
        for bad in ("", "../x.jsonl", "a/b.jsonl", ".metadata.json", ".hidden.jsonl", "x.json", "a..b.jsonl", "x.jsonl\n"):
            self.assertFalse(dataset.valid_dataset_filename(bad), bad)
            with self.assertRaises(ValueError):
                dataset.resolve_dataset_path(bad)


class LocalDatasetsTest(unittest.TestCase):
    """training_data: the datasets of a directory, found by their manifests."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def write(self, rel, content=""):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)

    def test_found_by_manifest(self):
        self.write("b-set/dataset.toml", 'file = "data/rows.jsonl"\ndescription = "d"\n')
        self.write("b-set/data/rows.jsonl", "{}\n")
        self.write("a-dir/dataset.toml", 'name = "alpha"\nfile = "x.jsonl"\n')
        self.write("a-dir/x.jsonl", "{}\n")
        self.write("a-dir/other.jsonl", "{}\n")          # the manifest decides which file
        self.write("no-manifest/rows.jsonl", "{}\n")     # not a dataset
        found, problems = training_data.list_local_datasets(self.root)
        self.assertEqual([d.name for d in found], ["alpha", "b-set"])
        self.assertEqual(problems, [])
        self.assertEqual(found[1].path, self.root / "b-set" / "data" / "rows.jsonl")
        self.assertEqual((found[1].description, found[1].filename), ("d", "b-set.jsonl"))
        self.assertEqual(training_data.find_local_dataset("alpha", self.root).path, self.root / "a-dir" / "x.jsonl")

    def test_problems(self):
        for d, manifest in {"bad-toml": "file = \n", "no-file": 'name = "nf"\n', "escape": 'file = "../x.jsonl"\n',
                            "absolute": 'file = "/etc/passwd"\n', "wrong-ext": 'file = "x.json"\n',
                            "bad-format": 'file = "x.jsonl"\nformat = "csv"\n', "bad name!": 'file = "x.jsonl"\n',
                            "missing": 'file = "x.jsonl"\ngenerator = "gen.py"\n'}.items():
            self.write(f"{d}/dataset.toml", manifest)
        self.write("ok/dataset.toml", 'file = "x.jsonl"\n')
        self.write("ok/x.jsonl", "{}\n")
        self.write("zdup/dataset.toml", 'name = "ok"\nfile = "x.jsonl"\n')
        self.write("zdup/x.jsonl", "{}\n")
        found, problems = training_data.list_local_datasets(self.root)
        self.assertEqual([d.name for d in found], ["ok"])
        self.assertEqual(sorted(p.split(":")[0] for p in problems),
                         sorted(["bad-toml", "no-file", "escape", "absolute", "wrong-ext", "bad-format", "bad name!", "missing", "zdup"]))
        self.assertTrue(any("run gen.py" in p for p in problems))

    def test_unknown(self):
        with self.assertRaises(training_data.DatasetError):
            training_data.find_local_dataset("nope", self.root)
        with self.assertRaises(training_data.DatasetError):
            training_data.find_local_dataset("nope", self.root / "missing-dir")


class FakeKV:
    """The part of a modal.Dict that EventLog uses; `fail` makes the next n requests raise."""

    def __init__(self):
        self.d, self.fail, self.requests = {}, 0, 0

    def _req(self):
        self.requests += 1
        if self.fail:
            self.fail -= 1
            raise ConnectionError("store down")

    def get(self, key, default=None):
        self._req()
        return self.d.get(key, default)

    def update(self, other):
        self._req()
        self.d.update(other)


class EventLogExportTest(unittest.TestCase):
    """EventLog.put_export: a file in chunks, then its description."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.kv = FakeKV()
        self.log = event_log.EventLog(self.kv, "j", sleep=lambda s: None)

    def write(self, data: bytes) -> str:
        p = Path(self.tmp.name, "f.zip")
        p.write_bytes(data)
        return str(p)

    def test_chunks_and_description(self):
        data = bytes(range(256)) * 10
        info = self.log.put_export(self.write(data), chunk_bytes=1000)
        self.assertEqual(info, {"bytes": 2560, "sha256": hashlib.sha256(data).hexdigest()})
        self.assertEqual(self.log.export_meta(), {**info, "chunks": 3})
        self.assertEqual(b"".join(self.log.read_export_chunk(n) for n in range(3)), data)
        self.assertEqual([len(self.log.read_export_chunk(n)) for n in range(3)], [1000, 1000, 560])
        self.assertIsNone(self.log.read_export_chunk(3))
        self.assertEqual(list(self.kv.d)[-1], "j:export")   # the description comes after the chunks

    def test_empty_and_too_large_files(self):
        with self.assertRaises(ValueError):
            self.log.put_export(self.write(b""))
        with self.assertRaises(ValueError):
            self.log.put_export(self.write(b"123456"), max_bytes=5)
        self.assertEqual(self.kv.d, {})

    def test_retries_a_failing_store_and_gives_up_without_a_description(self):
        self.kv.fail = 2
        self.log.put_export(self.write(b"abc"))
        self.assertEqual(self.log.export_meta()["chunks"], 1)
        kv = FakeKV()
        log = event_log.EventLog(kv, "k", retries=1, sleep=lambda s: None)
        kv.fail = 10
        with self.assertRaises(ConnectionError):
            log.put_export(self.write(b"abc"))
        kv.fail = 0
        self.assertIsNone(log.export_meta())

    def test_no_export(self):
        self.assertIsNone(self.log.export_meta())


class EventLogTest(unittest.TestCase):
    def setUp(self):
        self.kv = FakeKV()
        self.log = event_log.EventLog(self.kv, "j", chunk_size=3, sleep=lambda s: None)

    def test_append_and_read_across_chunks(self):
        self.assertEqual((self.log.head(), self.log.read_from(0), self.log.last_event()), (-1, [], None))
        for i in range(8):
            self.assertEqual(self.log.append({"type": "progress", "step": i}), i)
        self.assertEqual(self.log.head(), 7)
        self.assertEqual([(i, e["step"]) for i, e in self.log.read_from(0)], [(i, i) for i in range(8)])
        self.assertEqual([i for i, _ in self.log.read_from(4)], [4, 5, 6, 7])  # resume in the middle of a chunk
        self.assertEqual(self.log.read_from(8), [])
        self.assertEqual(self.log.last_event()["step"], 7)
        self.assertEqual(sorted(k for k in self.kv.d), ["j:c0", "j:c1", "j:c2", "j:head"])

    def test_a_restarted_writer_continues(self):
        for i in range(4):
            self.log.append({"step": i})
        again = event_log.EventLog(self.kv, "j", chunk_size=3, sleep=lambda s: None)
        self.assertEqual(again.append({"step": 4}), 4)
        self.assertEqual([e["step"] for _, e in again.read_from(0)], [0, 1, 2, 3, 4])
        self.assertEqual(event_log.EventLog(self.kv, "other").read_from(0), [])  # per job

    def test_events_without_type_are_progress(self):
        self.log.append({"step": 1})
        self.assertEqual(self.log.read_from(0)[0][1]["type"], "progress")

    def test_retries_then_raises(self):
        self.kv.fail = 3
        self.assertEqual(self.log.append({"step": 1}), 0)  # tried again
        self.kv.fail = 100
        with self.assertRaises(ConnectionError):
            self.log.append({"step": 2})
        self.kv.fail = 0
        self.assertEqual(self.log.append({"step": 3}), 1)  # the failed one is not in the log; no gap
        self.assertEqual([e["step"] for _, e in self.log.read_from(0)], [1, 3])

    def test_informing_events_give_up_fast(self):
        """A store that is down must not hold the training up: an informing event waits at most one back-off."""
        waits = []
        log = event_log.EventLog(self.kv, "w", chunk_size=3, sleep=waits.append)
        self.kv.fail = 100
        with self.assertRaises(ConnectionError):
            log.append({"step": 1}, retries=event_log.INFORMING_RETRIES)
        self.assertEqual(waits, [0.5])
        waits.clear()
        with self.assertRaises(ConnectionError):
            log.append({"step": 1})                      # a status event: the full back-off
        self.assertEqual(waits, [0.5, 1, 2, 4])

    def test_failed_tail_load_does_not_restart_the_log(self):
        """A restarted writer whose read of the tail fails halfway must not carry on from an empty chunk."""
        for i in range(4):
            self.log.append({"step": i})
        again = event_log.EventLog(self.kv, "j", chunk_size=3, retries=0, sleep=lambda s: None)
        self.kv.fail = 0
        real_get, calls = self.kv.get, []

        def flaky_get(key, default=None):
            calls.append(key)
            if len(calls) == 2:                          # the head is read, the chunk is not
                raise ConnectionError("store down")
            return real_get(key, default)
        self.kv.get = flaky_get
        with self.assertRaises(ConnectionError):
            again.append({"step": 4})
        self.assertEqual(again.append({"step": 4}), 4)   # the tail is loaded again: no event is overwritten
        self.assertEqual([e["step"] for _, e in again.read_from(0)], [0, 1, 2, 3, 4])

    def test_meta_stop_exists(self):
        self.assertFalse(self.log.exists())
        self.assertFalse(self.log.stop_requested())
        self.log.set_meta({"call_id": "fc-1"})
        self.assertTrue(self.log.exists())
        self.assertEqual(self.log.meta(), {"call_id": "fc-1"})
        self.log.request_stop()
        self.assertTrue(self.log.stop_requested())
        self.assertFalse(event_log.EventLog(self.kv, "other").stop_requested())
        only_events = event_log.EventLog(self.kv, "e")
        only_events.append({"step": 1})
        self.assertTrue(only_events.exists())


class ProgressEventsTest(unittest.TestCase):
    def setUp(self):
        self.kv = FakeKV()
        self.log = event_log.EventLog(self.kv, "p", sleep=lambda s: None)

    def read(self):
        return [e for _, e in self.log.read_from(0)]

    def test_write_and_read(self):
        progress_events.write_status(self.log, "running")
        progress_events.write_event(self.log, {"step": 5, "loss": float("nan"), "learning_rate": 1e-4})
        progress_events.write_status(self.log, "completed", final_loss=0.5)
        evs = self.read()
        self.assertEqual([e["type"] for e in evs], ["status", "progress", "status"])
        self.assertIsNone(evs[1]["loss"])  # NaN is not valid JSON: sent as null
        self.assertEqual(evs[1]["step"], 5)
        self.assertIn("timestamp", evs[0])
        self.assertFalse(progress_events.is_terminal(evs[0]))
        self.assertTrue(progress_events.is_terminal(evs[2]))

    def test_log_and_cancelled_events(self):
        progress_events.write_log(self.log, "loading the model")
        progress_events.write_status(self.log, "cancelled", error="stopped at step 3")
        evs = self.read()
        self.assertEqual((evs[0]["type"], evs[0]["message"]), ("log", "loading the model"))
        self.assertFalse(progress_events.is_terminal(evs[0]))
        self.assertTrue(progress_events.is_terminal(evs[1]))

    def test_no_log_reports_nothing(self):
        progress_events.write_status(None, "running")
        progress_events.write_log(None, "x")
        self.assertFalse(progress_events.stop_requested(None))

    def test_nested_non_finite_values_do_not_lose_the_event(self):
        progress_events.write_status(self.log, "completed", final_loss=float("nan"),
                                     model={"params": {"learning_rate": float("inf")}, "files": [float("nan")]})
        ev = self.log.read_from(0)[0][1]
        self.assertEqual((ev["final_loss"], ev["model"]), (None, {"params": {"learning_rate": None}, "files": [None]}))

    def test_best_effort_vs_status(self):
        self.kv.fail = 1000
        progress_events.write_event(self.log, {"step": 1}, best_effort=True)  # only printed
        progress_events.write_log(self.log, "x")
        with self.assertRaises(ConnectionError):
            progress_events.write_status(self.log, "completed")  # the status events must get through
        self.assertFalse(progress_events.stop_requested(self.log))  # an unreachable store is not a stop

    def test_stop_request(self):
        self.assertFalse(progress_events.stop_requested(self.log))
        progress_events.request_stop(self.log)
        self.assertTrue(progress_events.stop_requested(self.log))
        self.assertFalse(progress_events.stop_requested(event_log.EventLog(self.kv, "other")))  # per job

    def test_job_id(self):
        self.assertTrue(progress_events.valid_job_id("ab12-_"))
        for bad in ("", "../x", "a/b", "a b", "x" * 65):
            self.assertFalse(progress_events.valid_job_id(bad), bad)


class TrainedModelTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.models = self.tmp.name

    def save(self, job_id, files):
        d = Path(self.models, "trained", job_id)
        for name, content in files.items():
            (d / name).parent.mkdir(parents=True, exist_ok=True)
            (d / name).write_bytes(content)
        return str(d)

    def test_model_dir_is_per_job(self):
        self.assertEqual(trained_model.model_dir("j1"), f"{paths.MODELS_DIR}/trained/j1")
        self.assertNotEqual(trained_model.model_dir("j1"), trained_model.model_dir("j2"))
        with self.assertRaises(ValueError):
            trained_model.model_dir("../x")

    def test_describe(self):
        d = self.save("j1", {"adapter_config.json": b"{}", "adapter_model.safetensors": b"12345", "sub/tok.json": b"x"})
        ds = Path(self.models, "ds.jsonl")
        ds.write_bytes(b'{"a":1}\n')
        info = trained_model.describe(d, base_model="google/x", model_size="4b", num_examples=1, dataset_path=str(ds),
                                      params={"lora_rank": 16}, models_dir=self.models)
        self.assertEqual(info["path"], "trained/j1")
        self.assertEqual([f["path"] for f in info["files"]], ["adapter_config.json", "adapter_model.safetensors", "sub/tok.json"])
        self.assertEqual(info["size_bytes"], 2 + 5 + 1)
        import hashlib
        self.assertEqual(info["files"][1]["sha256"], hashlib.sha256(b"12345").hexdigest())
        self.assertEqual(info["dataset_sha256"], hashlib.sha256(b'{"a":1}\n').hexdigest())
        self.assertEqual((info["base_model"], info["model_size"], info["params"]), ("google/x", "4b", {"lora_rank": 16}))
        json.dumps(info, allow_nan=False)  # goes into a progress event

    def test_resolve_model_dir(self):
        d = self.save("j1", {"adapter_config.json": b"{}"})
        self.assertEqual(trained_model.resolve_model_dir("trained/j1", self.models), d)
        with self.assertRaises(FileNotFoundError):
            trained_model.resolve_model_dir("trained/nope", self.models)
        for bad in ("", "j1", "../trained/j1", "trained/../j1", "trained/j1/x", "/trained/j1"):
            with self.assertRaises(ValueError, msg=bad):
                trained_model.resolve_model_dir(bad, self.models)


class ValidModelFileTest(unittest.TestCase):
    def test_valid(self):
        for ok in ("adapter_model.safetensors", "adapter_config.json", "sub/dir/tokenizer.model", "a-b_c.d"):
            self.assertTrue(trained_model.valid_model_file(ok), ok)

    def test_invalid(self):
        for bad in ("", ".", "..", "../x", "a/../b", "/etc/passwd", "a//b", "a/", ".hidden", "a b", "a\\b", "x" * 5 + "/y" * 8, None, 3):
            self.assertFalse(trained_model.valid_model_file(bad), bad)


class RunTrainTest(unittest.TestCase):
    """bootstrap.run_train reports the job's life to its event log (the trainers themselves are faked)."""

    def setUp(self):
        from ai_train_core import bootstrap
        self.bootstrap = bootstrap
        self.kv = FakeKV()

    def run_train(self, *args, **kw):
        return self.bootstrap.run_train(*args, event_kv=self.kv, **kw)

    def statuses(self, job_id):
        return [(e["status"], e) for _, e in event_log.EventLog(self.kv, job_id).read_from(0) if e["type"] == "status"]

    def test_completed(self):
        fake = mock.Mock()
        model = {"path": "trained/jc", "files": [], "size_bytes": 0}
        fake.run_finetuning.return_value = {"status": "success", "final_loss": 0.25, "model": model}
        with mock.patch.dict(sys.modules, {"ai_train_core.gemma4.model_train": fake}):
            self.run_train(3, "4b", dataset="mine.jsonl", job_id="jc")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["dataset_path"], f"{paths.DATA_DIR}/mine.jsonl")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["job_id"], "jc")
        st = self.statuses("jc")
        self.assertEqual([s for s, _ in st], ["running", "completed"])
        self.assertEqual(st[1][1]["final_loss"], 0.25)
        self.assertEqual(st[1][1]["model"], model)  # the weights the job saved ride on the completed event

    def test_failed(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = RuntimeError("out of memory")
        with mock.patch.dict(sys.modules, {"ai_train_core.gemma4.model_train": fake}):
            with self.assertRaises(RuntimeError):
                self.run_train(3, "4b", dataset="mine.jsonl", job_id="jf")
        st = self.statuses("jf")
        self.assertEqual([s for s, _ in st], ["running", "failed"])
        self.assertIn("out of memory", st[1][1]["error"])

    def test_cancelled(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = progress_events.JobCancelled("stopped at step 4")
        with mock.patch.dict(sys.modules, {"ai_train_core.gemma4.model_train": fake}):
            result = self.run_train(3, "4b", dataset="mine.jsonl", job_id="jx")  # not an error: nothing is raised
        self.assertEqual(result["status"], "cancelled")
        st = self.statuses("jx")
        self.assertEqual([s for s, _ in st], ["running", "cancelled"])
        self.assertIn("step 4", st[-1][1]["error"])

    def test_the_trainer_gets_the_event_log(self):
        fake = mock.Mock()
        fake.run_finetuning.return_value = {"status": "success", "final_loss": 0.1}
        with mock.patch.dict(sys.modules, {"ai_train_core.gemma4.model_train": fake}):
            self.run_train(3, "4b", dataset="mine.jsonl", job_id="jr")
        events = fake.run_finetuning.call_args.kwargs["events"]
        self.assertEqual((events.kv, events.job_id), (self.kv, "jr"))

    def test_a_failing_store_does_not_hide_the_error(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = RuntimeError("out of memory")
        with mock.patch.dict(sys.modules, {"ai_train_core.gemma4.model_train": fake}):
            self.kv.fail = 0
            real = self.kv.update
            calls = []

            def flaky(other):
                calls.append(1)
                if len(calls) > 2:  # "running" and the log line get in; the "failed" event does not
                    raise ConnectionError("store down")
                real(other)
            self.kv.update = flaky
            with mock.patch("ai_train_core.event_log.time.sleep"), self.assertRaises(RuntimeError):
                self.run_train(3, "4b", dataset="mine.jsonl", job_id="jq")

    def test_bad_dataset_fails_the_job(self):
        for i, bad in enumerate(("../x.jsonl", "")):  # a bad name, and no dataset at all (there is no default)
            with self.assertRaises(ValueError):
                self.run_train(3, "4b", dataset=bad, job_id=f"jb{i}")
            self.assertEqual([s for s, _ in self.statuses(f"jb{i}")], ["running", "failed"])


class ApiTest(unittest.TestCase):
    def setUp(self):
        try:
            from fastapi.testclient import TestClient
            from api.api import create_api
        except ImportError:
            self.skipTest("fastapi/httpx not installed")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.data = Path(self.tmp.name, "data")
        self.data.mkdir()
        (self.data / "mine.jsonl").write_text("{}\n")  # a dataset every job can name
        p = mock.patch("api.api.DATA_DIR", str(self.data))
        p.start()
        self.addCleanup(p.stop)
        self.kv = FakeKV()
        self.call_state = CallStatus("running")
        self.calls, self.commits = [], []
        self.spawn_error = None

        def spawn(steps, size, ds, job_id):
            if self.spawn_error:
                raise self.spawn_error
            self.calls.append((steps, size, ds, job_id))
            return "fc-1"
        self.agent_calls = []

        def spawn_agent(topic, max_steps, model, ds, job_id):
            if self.spawn_error:
                raise self.spawn_error
            self.agent_calls.append((topic, max_steps, model, ds, job_id))
            return "fc-agent"
        self.spawn_agent = spawn_agent
        api = create_api(
            spawn_training_job=spawn,
            infer_response=lambda *a: {"response": "ok", "args": list(a)},
            commit_volumes=self.commits.append,
            reload_volumes=lambda p: None,
            event_kv=self.kv,
            call_status=lambda call_id: self.call_state,
            spawn_agent_job=spawn_agent,
            poll_interval_s=0.01,
            max_stream_s=0.3,
            watchdog_s=0.05,
        )
        self.client = TestClient(api)

    def log(self, job_id):
        return event_log.EventLog(self.kv, job_id, sleep=lambda s: None)

    def events(self, job_id, query=""):
        """(sse id, event name, data) of every event the stream sends before it ends."""
        r = self.client.get(f"/training/job_events?job_id={job_id}{query}")
        self.assertEqual(r.status_code, 200)
        self.assertTrue(r.headers["content-type"].startswith("text/event-stream"))
        out = []
        for block in r.text.split("\n\n"):
            fields = dict(l.split(": ", 1) for l in block.split("\n") if l and not l.startswith(":"))
            if fields:
                out.append((int(fields["id"]), fields["event"], json.loads(fields["data"])))
        return out

    def test_healthz(self):
        self.assertEqual(self.client.get("/healthz").json()["status"], "ok")

    def test_job_start(self):
        r = self.client.post("/training/job_start", json={"dataset": "mine.jsonl", "num_train_steps": 7, "model_size": "12b"})
        job = r.json()
        self.assertEqual((job["provider_job_id"], job["status"], job["model_size"], job["num_train_steps"], job["dataset"]),
                         ("fc-1", "started", "12b", 7, "mine.jsonl"))
        started = datetime.fromisoformat(job["started_at"].replace("Z", "+00:00"))
        self.assertIsNotNone(started.tzinfo)  # golang needs a timezone to parse it
        self.assertTrue(progress_events.valid_job_id(job["job_id"]))
        self.assertEqual(self.calls, [(7, "12b", "mine.jsonl", job["job_id"])])  # the job id names the event log
        self.assertEqual(self.log(job["job_id"]).meta()["call_id"], "fc-1")  # for the watchdog

    def test_job_start_dataset(self):
        job = self.client.post("/training/job_start", json={"dataset": "mine.jsonl"}).json()
        self.assertEqual(job["dataset"], "mine.jsonl")
        self.assertEqual(self.calls[0][2], "mine.jsonl")
        n = len(self.calls)
        for bad in ("missing.jsonl", "../mine.jsonl", "mine.txt"):
            self.assertEqual(self.client.post("/training/job_start", json={"dataset": bad}).status_code, 400, bad)
        self.assertEqual(len(self.calls), n)  # nothing was spawned

    def test_job_start_stores_inline_dataset(self):
        content = base64.b64encode(b'{"instruction":"a","output":"b"}\n').decode()
        job = self.client.post("/training/job_start", json={"dataset": "new.jsonl", "dataset_content": content}).json()
        self.assertEqual(job["dataset"], "new.jsonl")
        self.assertEqual((self.data / "new.jsonl").read_bytes(), b'{"instruction":"a","output":"b"}\n')
        self.assertEqual(self.commits, [[str(self.data)]])
        n = len(self.calls)
        for body in ({"dataset": "x.jsonl", "dataset_content": "not base64!"},
                     {"dataset": "../x.jsonl", "dataset_content": content},
                     {"dataset_content": content}):  # a dataset's content needs its name
            self.assertEqual(self.client.post("/training/job_start", json=body).status_code, 400, body)
        self.assertEqual(len(self.calls), n)
        self.assertFalse((self.data / "x.jsonl").exists())

    def test_training_dataset_model(self):
        from api.models import TrainingDataset
        path = self.data / "m.jsonl"
        path.write_bytes(b"x\n")
        ds = TrainingDataset.from_path(str(path), with_content=True)
        self.assertEqual((ds.filename, ds.size_bytes, ds.payload()), ("m.jsonl", 2, b"x\n"))
        self.assertNotIn("content", ds.model_dump())  # never serialized
        self.assertIsNotNone(ds.modified_at.tzinfo)
        self.assertIsNone(TrainingDataset.from_path(str(path)).content)
        for bad in (None, "", "not base64!"):
            with self.assertRaises(ValueError):
                TrainingDataset(filename="m.jsonl", content=bad).payload()

    def test_no_dataset_endpoints(self):
        self.assertEqual(self.client.post("/datasets/upload", json={"filename": "a.jsonl", "content": "x"}).status_code, 404)
        self.assertEqual(self.client.get("/datasets/list").status_code, 404)

    def test_job_events_stream(self):
        log = self.log("j1")
        progress_events.write_status(log, "running")
        progress_events.write_event(log, {"type": "progress", "step": 5, "loss": 1.5})
        progress_events.write_status(log, "completed", final_loss=1.2)
        evs = self.events("j1")
        self.assertEqual([(i, e) for i, e, _ in evs], [(0, "status"), (1, "progress"), (2, "status")])
        self.assertEqual(evs[1][2]["loss"], 1.5)
        self.assertEqual(evs[2][2]["final_loss"], 1.2)
        # Resuming: only the events from the given index on.
        self.assertEqual([i for i, _, _ in self.events("j1", "&from=2")], [2])
        self.assertEqual(self.events("j1", "&from=3"), [])  # past the end of a finished job: ends after max_stream_s

    def test_job_events_follows_a_running_job(self):
        log = self.log("j2")
        self.assertEqual(self.events("j2"), [])  # no events yet: waits, then gives up when max_stream_s is over
        progress_events.write_status(log, "running")
        self.assertEqual([e["status"] for _, _, e in self.events("j2")], ["running"])  # not finished: stream times out
        self.assertEqual(self.client.get("/training/job_events").status_code, 400)
        self.assertEqual(self.client.get("/training/job_events?job_id=../x").status_code, 400)

    def test_job_events_survive_an_unreachable_store(self):
        progress_events.write_status(self.log("j3"), "completed")
        self.kv.fail = 4  # the first reads fail, the stream keeps trying
        with mock.patch("ai_train_core.event_log.time.sleep"):
            self.assertEqual([e for _, e, _ in self.events("j3")], ["status"])

    def test_a_reader_that_was_away_gets_what_it_missed(self):
        log = self.log("j4")
        progress_events.write_status(log, "training")
        first = self.events("j4")
        for step in range(1, 120):  # more than two chunks
            progress_events.write_event(log, {"type": "progress", "step": step})
        progress_events.write_status(log, "completed")
        rest = self.events("j4", f"&from={first[-1][0] + 1}")
        self.assertEqual([i for i, _, _ in rest], list(range(1, 121)))
        self.assertEqual(rest[-1][2]["status"], "completed")

    def test_watchdog_reports_a_job_that_died(self):
        log = self.log("w1")
        self.client.post("/training/job_start", json={"dataset": "mine.jsonl"})  # (a job of its own: the id below is set by hand)
        log.set_meta({"call_id": "fc-9"})
        progress_events.write_status(log, "training", max_steps=10)
        self.assertEqual([s for _, _, s in self.events("w1")], [e for _, e in log.read_from(0)])  # running: nothing added
        self.call_state = CallStatus("failed", "OutOfMemoryError: CUDA out of memory")
        evs = self.events("w1")
        self.assertEqual([e for _, e, _ in evs], ["status", "status"])
        self.assertEqual(evs[1][2]["status"], "failed")
        self.assertIn("CUDA out of memory", evs[1][2]["error"])
        self.assertEqual(len(log.read_from(0)), 2)
        self.assertEqual(len(self.events("w1")), 2)  # a second stream does not add another

    def test_watchdog_leaves_a_job_that_reported_its_end(self):
        log = self.log("w2")
        log.set_meta({"call_id": "fc-9"})
        progress_events.write_status(log, "failed", error="boom")
        self.call_state = CallStatus("failed", "RuntimeError: boom")  # the call fails after the event was written
        self.assertEqual(len(self.events("w2")), 1)
        self.assertEqual(len(log.read_from(0)), 1)

    def test_watchdog_needs_the_call_id(self):
        progress_events.write_status(self.log("w3"), "training")
        self.call_state = CallStatus("failed", "x")
        self.assertEqual(len(self.events("w3")), 1)  # a job not started through job_start has no call id: left alone

    def test_job_cancel(self):
        log = self.log("c1")
        post = lambda body: self.client.post("/training/job_cancel", json=body)
        self.assertEqual(post({}).status_code, 400)
        self.assertEqual(post({"job_id": "../x"}).status_code, 400)
        self.assertEqual(post({"job_id": "c1"}).status_code, 404)  # no such job
        progress_events.write_status(log, "training", max_steps=10)
        r = post({"job_id": "c1"})
        self.assertEqual((r.status_code, r.json()), (200, {"job_id": "c1", "status": "stopping"}))
        self.assertTrue(progress_events.stop_requested(log))
        self.assertFalse(progress_events.stop_requested(self.log("c2")))
        self.assertEqual(len(log.read_from(0)), 1)  # the API itself writes no event
        progress_events.write_status(log, "cancelled")
        self.assertEqual(post({"job_id": "c1"}).status_code, 409)  # over already

    def test_job_cancel_before_the_container_starts(self):
        job = self.client.post("/training/job_start", json={"dataset": "mine.jsonl"}).json()
        r = self.client.post("/training/job_cancel", json={"job_id": job["job_id"]})
        self.assertEqual(r.status_code, 200)  # no event yet, but the job exists
        self.assertTrue(progress_events.stop_requested(self.log(job["job_id"])))

    def test_job_progress(self):
        self.assertEqual(self.client.get("/training/job_progress").status_code, 400)
        self.assertEqual(self.client.get("/training/job_progress?job_id=a").json(), {"progress": []})
        progress_events.write_event(self.log("a"), {"step": 1})
        progress_events.write_event(self.log("b"), {"step": 2})
        self.assertEqual(self.client.get("/training/job_progress?job_id=a").json()["progress"][0]["step"], 1)

    def test_job_start_defaults_and_invalid_steps(self):
        job = self.client.post("/training/job_start", json={"dataset": "mine.jsonl"}).json()
        self.assertEqual((job["model_size"], job["num_train_steps"]), ("4b", 100))
        for bad in (0, -1, "5", True):
            body = {"dataset": "mine.jsonl", "num_train_steps": bad}
            self.assertEqual(self.client.post("/training/job_start", json=body).status_code, 400)

    def test_job_start_that_cannot_spawn_closes_the_log(self):
        """No container, no call id: the watchdog could never tell, so the API ends the job's log itself."""
        self.spawn_error = RuntimeError("modal is down")
        client = type(self.client)(self.client.app, raise_server_exceptions=False)
        self.assertEqual(client.post("/training/job_start", json={"dataset": "mine.jsonl"}).status_code, 500)
        jobs = {k.split(":")[0] for k in self.kv.d}
        self.assertEqual(len(jobs), 1)
        events = self.log(jobs.pop()).read_from(0)
        self.assertEqual([(e["type"], e["status"]) for _, e in events], [("status", "failed")])
        self.assertIn("modal is down", events[0][1]["error"])

    def test_job_start_needs_a_dataset(self):
        n = len(self.calls)
        for body in ({}, {"dataset": ""}, {"model_size": "4b"}):
            r = self.client.post("/training/job_start", json=body)
            self.assertEqual(r.status_code, 400, body)
            self.assertIn("dataset is required", r.json()["error"])
        self.assertEqual(len(self.calls), n)

    def test_invalid_model_size(self):
        self.assertEqual(self.client.post("/training/job_start", json={"dataset": "a.jsonl", "model_size": "99b"}).status_code, 400)
        self.assertEqual(self.client.post("/infer/query", json={"model_size": "99b"}).status_code, 400)

    def test_infer(self):
        r = self.client.post("/infer/query", json={"prompt": "hi"})
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "4b", ""])

    def test_infer_with_a_trained_model(self):
        r = self.client.post("/infer/query", json={"prompt": "hi", "model_size": "12b", "model_path": "trained/abc123"})
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "12b", "trained/abc123"])
        for bad in ("../x", "trained/../x", "/models/trained/a", "finetuned_model_4b", "trained/a/b", "trained/"):
            r = self.client.post("/infer/query", json={"prompt": "hi", "model_path": bad})
            self.assertEqual(r.status_code, 400, bad)

    # ---- research jobs (the gligabao_agent binary in Modal) ----

    def test_research_job_start(self):
        r = self.client.post("/research/job_start", json={"topic": "  batteries  ", "max_steps": 7, "model": "m", "dataset": "d"})
        self.assertEqual(r.status_code, 200, r.text)
        job = r.json()
        self.assertEqual((job["provider_job_id"], job["status"]), ("fc-agent", "started"))
        self.assertTrue(progress_events.valid_job_id(job["job_id"]))
        self.assertEqual(self.agent_calls, [("batteries", 7, "m", "d", job["job_id"])])
        self.assertEqual(self.log(job["job_id"]).meta()["call_id"], "fc-agent")  # for the watchdog
        r = self.client.post("/research/job_start", json={"topic": "t"})  # defaults
        self.assertEqual(self.agent_calls[-1][1:4], (20, "", ""))

    def test_research_job_start_validates(self):
        for body in ({}, {"topic": " "}, {"topic": 5}, {"topic": "t", "max_steps": 0}, {"topic": "t", "max_steps": 41},
                     {"topic": "t", "max_steps": "9"}, {"topic": "t", "model": "a b"}, {"topic": "t", "dataset": "a b"}):
            self.assertEqual(self.client.post("/research/job_start", json=body).status_code, 400, body)
        self.assertEqual(self.client.post("/research/job_start", content=b"[1]").status_code, 400)
        self.assertEqual(self.agent_calls, [])

    def test_research_job_start_without_the_agent(self):
        from api.api import create_api
        client = type(self.client)(create_api(spawn_training_job=None, infer_response=None, commit_volumes=None, reload_volumes=None,
                                              event_kv=self.kv, call_status=None))
        r = client.post("/research/job_start", json={"topic": "t"})
        self.assertEqual(r.status_code, 503)
        self.assertIn("gligabao_agent", r.json()["error"])
        self.assertEqual(self.kv.d, {})  # no job was made

    def test_research_job_that_cannot_spawn_closes_the_log(self):
        self.spawn_error = RuntimeError("modal is down")
        client = type(self.client)(self.client.app, raise_server_exceptions=False)
        self.assertEqual(client.post("/research/job_start", json={"topic": "t"}).status_code, 500)
        jobs = {k.split(":")[0] for k in self.kv.d}
        self.assertEqual(len(jobs), 1)
        events = self.log(jobs.pop()).read_from(0)
        self.assertEqual([(e["type"], e["status"]) for _, e in events], [("status", "failed")])

    def test_research_job_events_and_watchdog(self):
        log = self.log("r1")
        progress_events.write_status(log, "running")
        progress_events.write_event(log, {"type": "state", "agent": "main", "state": "searching"})
        progress_events.write_status(log, "completed", report="R")
        r = self.client.get("/research/job_events?job_id=r1&from=1")
        self.assertEqual(r.status_code, 200)
        self.assertIn("id: 1\nevent: state\n", r.text)
        self.assertIn('id: 2\nevent: status\ndata: {"type": "status", "status": "completed", "report": "R"', r.text)
        self.assertNotIn("id: 0", r.text)
        # a job whose call ended without a final event is closed by the watchdog, which names the kind of job
        job = self.client.post("/research/job_start", json={"topic": "t"}).json()["job_id"]
        self.call_state = CallStatus("failed", "OOM")
        self.client.get(f"/research/job_events?job_id={job}")
        last = self.log(job).last_event()
        self.assertEqual(last["status"], "failed")
        self.assertIn("research container ended without reporting it", last["error"])

    def test_research_job_cancel(self):
        job = self.client.post("/research/job_start", json={"topic": "t"}).json()["job_id"]
        r = self.client.post("/research/job_cancel", json={"job_id": job})
        self.assertEqual((r.status_code, r.json()["status"]), (200, "stopping"))
        self.assertTrue(self.log(job).stop_requested())
        self.assertEqual(self.client.post("/research/job_cancel", json={"job_id": "nope"}).status_code, 404)
        self.assertEqual(self.client.post("/research/job_cancel", json={}).status_code, 400)
        progress_events.write_status(self.log(job), "cancelled")
        self.assertEqual(self.client.post("/research/job_cancel", json={"job_id": job}).status_code, 409)

    def test_research_job_export(self):
        data = os.urandom(10_000)
        path = Path(tempfile.mkdtemp(), "export.zip")
        path.write_bytes(data)
        self.addCleanup(path.unlink)
        info = self.log("r2").put_export(str(path), chunk_bytes=4096)
        r = self.client.get("/research/job_export?job_id=r2")
        self.assertEqual(r.status_code, 200)
        self.assertEqual(r.headers["content-type"], "application/zip")
        self.assertEqual(r.headers["content-length"], "10000")
        self.assertEqual(r.content, data)
        self.assertEqual(hashlib.sha256(r.content).hexdigest(), info["sha256"])
        # no export, a job that does not exist, a bad id
        progress_events.write_status(self.log("r3"), "completed", report="R")
        self.assertEqual(self.client.get("/research/job_export?job_id=r3").status_code, 404)
        self.assertEqual(self.client.get("/research/job_export?job_id=nope").status_code, 404)
        self.assertEqual(self.client.get("/research/job_export?job_id=a/b").status_code, 400)
        self.assertEqual(self.client.get("/research/job_export").status_code, 400)


FAKE_AGENT = """#!/usr/bin/env python3
import json, os, signal, sys, time
mode = os.environ.get("FAKE_AGENT_MODE", "ok")
topic = sys.stdin.read()
ev = lambda **kw: print(json.dumps({"event": kw.pop("event"), **kw}, separators=(",", ":")), file=sys.stderr, flush=True)
with open(os.environ["FAKE_AGENT_ARGS"], "w") as f:
    json.dump({"argv": sys.argv[1:], "topic": topic, "cwd": os.getcwd()}, f)
def export():
    if "--export" in sys.argv and not os.environ.get("FAKE_AGENT_NO_EXPORT"):
        with open(sys.argv[sys.argv.index("--export") + 1], "wb") as f:
            f.write(b"PK-fake-export:" + topic.encode())
if mode == "ok":
    ev(event="state", agent="main", state_seq=2, state="searching", step=1, detail="q", error="")
    print("gligabao_agent: a warning", file=sys.stderr, flush=True)
    ev(event="state", agent="kid", state_seq=2, state="reading", step=1, detail="u", error="")
    ev(event="usage", steps=2, tool_calls=1, input_tokens=10, output_tokens=5, cost_usd=0.5, subagents=1)
    print(json.dumps({"report": "REPORT of " + topic, "run_id": 7, "subagents": 1, "steps": 3, "tool_calls": 2,
                      "input_tokens": 100, "output_tokens": 50, "cost_usd": 0.75}))
    export()
elif mode == "export_failed":   # the report is there, the export is not: the binary exits with 1
    print(json.dumps({"report": "REPORT", "run_id": 7}))
    print("gligabao_agent: export: disk full", file=sys.stderr, flush=True)
    sys.exit(1)
elif mode == "fail":
    print("gligabao_agent: the research failed: anthropic is overloaded", file=sys.stderr, flush=True)
    export()
    sys.exit(1)
elif mode == "garbage":
    print("not json")
elif mode == "hang":
    def stop(*a):
        export()
        sys.exit(1)
    signal.signal(signal.SIGTERM, stop)
    ev(event="state", agent="main", state_seq=2, state="thinking", step=1, detail="", error="")
    time.sleep(60)
"""


class AgentRunTest(unittest.TestCase):
    """ai_train_core/agent_run.py with a fake binary: a script that speaks the --events protocol of gligabao_agent."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.binary = Path(self.tmp.name, "gligabao_agent")
        self.binary.write_text(FAKE_AGENT)
        self.binary.chmod(0o755)
        self.args_file = Path(self.tmp.name, "args.json")
        self.kv = FakeKV()
        for k, v in (("FAKE_AGENT_ARGS", str(self.args_file)), ("FAKE_AGENT_MODE", "ok")):
            p = mock.patch.dict(os.environ, {k: v})
            p.start()
            self.addCleanup(p.stop)

    def run_job(self, **kw):
        args = dict(topic="batteries", max_steps=7, model="m", dataset="d", job_id="j1", event_kv=self.kv, timeout_s=1800,
                    binary=str(self.binary))
        args.update(kw)
        return agent_run.run_agent_job(**args)

    def events(self, job_id="j1"):
        return [e for _, e in event_log.EventLog(self.kv, job_id).read_from(0)]

    def test_completed(self):
        result = self.run_job()
        self.assertEqual(result["status"], "completed")
        evs = self.events()
        self.assertEqual(evs[0], {"type": "status", "status": "running", "timestamp": evs[0]["timestamp"]})
        states = [(e["agent"], e["state_seq"], e["state"], e["detail"]) for e in evs if e["type"] == "state"]
        self.assertEqual(states, [("main", 2, "searching", "q"), ("kid", 2, "reading", "u")])
        self.assertIn("gligabao_agent: a warning", [e["message"] for e in evs if e["type"] == "log"])
        usage = [e["usage"] for e in evs if e["type"] == "usage"]
        self.assertEqual(usage, [{"steps": 2, "tool_calls": 1, "input_tokens": 10, "output_tokens": 5, "cost_usd": 0.5, "subagents": 1}])
        last = evs[-1]
        self.assertEqual((last["type"], last["status"], last["report"]), ("status", "completed", "REPORT of batteries"))
        self.assertEqual(last["usage"], {"steps": 3, "tool_calls": 2, "input_tokens": 100, "output_tokens": 50, "cost_usd": 0.75, "subagents": 1})
        # the final status comes after every event the binary wrote
        # (the log event between them says that the export of the agent is stored, which comes before the final event)
        self.assertEqual([e["type"] for e in evs][-3:], ["usage", "log", "status"])
        self.assertTrue(event_log.EventLog(self.kv, "j1").exists())

    def test_command_line(self):
        self.run_job()
        got = json.loads(self.args_file.read_text())
        self.assertEqual(got["topic"], "batteries")     # on stdin
        argv = got["argv"]
        self.assertEqual(argv[-1], "-")
        for flag, value in (("--max-steps", "7"), ("--model", "m"), ("--dataset", "d"), ("--timeout", f"{1800 - agent_run.TIMEOUT_MARGIN_S}s")):
            self.assertEqual(argv[argv.index(flag) + 1], value)
        self.assertIn("--json", argv)
        self.assertIn("--events", argv)
        db = argv[argv.index("--db") + 1]
        self.assertTrue(db.startswith(got["cwd"]) and db.endswith("gligabao.db"))   # in a directory of the job
        self.assertFalse(os.path.exists(got["cwd"]))                                # which is removed afterwards

    def test_export_is_stored_and_reported(self):
        result = self.run_job()
        self.assertEqual(result["status"], "completed")
        argv = json.loads(self.args_file.read_text())["argv"]
        self.assertEqual(os.path.basename(argv[argv.index("--export") + 1]), agent_run.EXPORT_FILE)
        log = event_log.EventLog(self.kv, "j1")
        data = b"PK-fake-export:batteries"
        meta = log.export_meta()
        self.assertEqual(meta, {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest(), "chunks": 1})
        self.assertEqual(log.read_export_chunk(0), data)
        evs = self.events()
        self.assertEqual(evs[-1]["export"], {"bytes": len(data), "sha256": meta["sha256"]})
        self.assertIn("export", [e["message"].split()[1] for e in evs if e["type"] == "log" and e["message"].startswith("the export")])

    def test_export_of_a_failed_job_is_kept(self):
        os.environ["FAKE_AGENT_MODE"] = "fail"
        self.assertEqual(self.run_job()["status"], "failed")
        last = self.events()[-1]
        self.assertEqual(last["status"], "failed")
        self.assertEqual(last["export"]["bytes"], len(b"PK-fake-export:batteries"))

    def test_no_export_file_no_export_field(self):
        os.environ["FAKE_AGENT_NO_EXPORT"] = "1"
        self.assertEqual(self.run_job()["status"], "completed")
        self.assertNotIn("export", self.events()[-1])
        self.assertIsNone(event_log.EventLog(self.kv, "j1").export_meta())

    def test_export_that_cannot_be_stored_does_not_fail_the_job(self):
        with mock.patch.object(event_log, "EXPORT_MAX_BYTES", 5):
            result = self.run_job()
        self.assertEqual(result["status"], "completed")
        evs = self.events()
        self.assertNotIn("export", evs[-1])
        self.assertTrue(any("could not be stored" in e.get("message", "") for e in evs))

    def test_report_without_export_is_still_a_completed_job(self):
        os.environ["FAKE_AGENT_MODE"] = "export_failed"
        self.assertEqual(self.run_job()["status"], "completed")
        evs = self.events()
        self.assertEqual((evs[-1]["status"], evs[-1]["report"]), ("completed", "REPORT"))
        self.assertNotIn("export", evs[-1])
        self.assertTrue(any("export: disk full" in e.get("message", "") for e in evs))

    def test_stopped_job_keeps_its_export(self):
        os.environ["FAKE_AGENT_MODE"] = "hang"
        log = event_log.EventLog(self.kv, "j1")

        def stop_when_working():
            for _ in range(500):
                if any(e.get("state") == "thinking" for _, e in log.read_from(0)):
                    log.request_stop()
                    return
                time.sleep(0.02)
        t = threading.Thread(target=stop_when_working, daemon=True)
        t.start()
        with mock.patch.object(agent_run, "STOP_POLL_S", 0.05):
            self.assertEqual(self.run_job()["status"], "cancelled")
        t.join()
        self.assertIn("export", self.events()[-1])

    def test_optional_flags_are_left_out(self):
        cmd = agent_run.build_command("/b", "/w", 5, "", "", 0)
        self.assertEqual(cmd, ["/b", "--json", "--events", "--max-steps", "5", "--db", "/w/gligabao.db", "-"])

    def test_failed(self):
        os.environ["FAKE_AGENT_MODE"] = "fail"
        self.assertEqual(self.run_job()["status"], "failed")
        last = self.events()[-1]
        self.assertEqual((last["type"], last["status"]), ("status", "failed"))
        self.assertIn("exited with code 1", last["error"])
        self.assertIn("anthropic is overloaded", last["error"])

    def test_unreadable_result(self):
        os.environ["FAKE_AGENT_MODE"] = "garbage"
        self.assertEqual(self.run_job()["status"], "failed")
        self.assertIn("without a readable result", self.events()[-1]["error"])

    def test_missing_binary(self):
        self.assertEqual(self.run_job(binary=str(self.binary) + ".nope")["status"], "failed")
        self.assertIn("build it with ./run_build.sh", self.events()[-1]["error"])

    def test_stop_before_start(self):
        event_log.EventLog(self.kv, "j1").request_stop()
        self.assertEqual(self.run_job()["status"], "cancelled")
        self.assertFalse(self.args_file.exists())   # the binary never ran
        self.assertEqual(self.events()[-1]["status"], "cancelled")

    def test_stop_while_running(self):
        os.environ["FAKE_AGENT_MODE"] = "hang"
        log = event_log.EventLog(self.kv, "j1")

        def stop_when_working():
            for _ in range(500):
                if any(e.get("state") == "thinking" for _, e in log.read_from(0)):
                    log.request_stop()
                    return
                time.sleep(0.02)
        t = threading.Thread(target=stop_when_working, daemon=True)
        t.start()
        with mock.patch.object(agent_run, "STOP_POLL_S", 0.05):
            result = self.run_job()
        t.join()
        self.assertEqual(result["status"], "cancelled")
        last = self.events()[-1]
        self.assertEqual((last["type"], last["status"]), ("status", "cancelled"))

    def test_convert_event(self):
        self.assertIsNone(agent_run.convert_event("gligabao_agent: something"))
        self.assertIsNone(agent_run.convert_event('{"event":"state", broken'))
        self.assertIsNone(agent_run.convert_event('{"event":"other"}'))
        self.assertEqual(agent_run.convert_event('{"event":"state","state":"reading"}'),
                         {"type": "state", "agent": "main", "state_seq": 0, "state": "reading", "step": 0, "detail": "", "error": ""})


class InferenceCacheTest(unittest.TestCase):
    """The loaded model stays in the container: only a different model (or a retrained adapter) loads again."""

    def setUp(self):
        from ai_train_core import inference
        self.inference = inference
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        inference._cache.clear()
        self.addCleanup(inference._cache.clear)

    def adapter(self, name):
        d = Path(self.dir.name) / name
        d.mkdir()
        (d / "adapter_config.json").write_text("{}")
        return str(d)

    def test_loads_once_per_model(self):
        a, b = self.adapter("a"), self.adapter("b")
        load = mock.Mock(side_effect=lambda size, seq, q4, d, family: (object(), object(), {}, d))
        with mock.patch.object(self.inference, "_load", load):
            first = self.inference._get_model("4b", a)
            again = self.inference._get_model("4b", a)
            self.assertTrue(first[4])
            self.assertFalse(again[4])
            self.assertIs(first[0], again[0])
            self.assertEqual(load.call_count, 1)
            self.assertTrue(self.inference._get_model("4b", b)[4])     # another model replaces it
            self.assertEqual(load.call_count, 2)
            self.assertTrue(self.inference._get_model("4b", a)[4])
            os.utime(Path(a) / "adapter_config.json", ns=(1, 1))        # retrained in place
            self.assertTrue(self.inference._get_model("4b", a)[4])

    def test_missing_adapter_is_not_cached(self):
        load = mock.Mock(side_effect=FileNotFoundError("no adapter"))
        with mock.patch.object(self.inference, "_load", load):
            for _ in range(2):
                with self.assertRaises(FileNotFoundError):
                    self.inference._get_model("4b", str(Path(self.dir.name) / "none"))
            self.assertEqual(load.call_count, 2)


if __name__ == "__main__":
    unittest.main()
