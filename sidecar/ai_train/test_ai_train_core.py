"""Local unit tests for the provider-neutral training code. No GPU, Modal or ML libs needed.

    cd sidecar/ai_train && python -m unittest test_ai_train_core -v
"""
import base64
import json
import os
import sys
import tempfile
import unittest
from datetime import datetime
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from unittest import mock

import training_data
from ai_train_core import dataset, event_log, paths, progress_events, trained_model
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
        """Every dataset of bin/datasets_training whose data file has been generated is valid."""
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
        api = create_api(
            spawn_training_job=lambda steps, size, ds, job_id: self.calls.append((steps, size, ds, job_id)) or "fc-1",
            infer_response=lambda *a: {"response": "ok", "args": list(a)},
            commit_volumes=self.commits.append,
            reload_volumes=lambda p: None,
            event_kv=self.kv,
            call_status=lambda call_id: self.call_state,
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
