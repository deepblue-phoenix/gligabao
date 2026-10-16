"""Local unit tests for the provider-neutral training code. No GPU, Modal or ML libs needed.

    cd side_car/ai_train && python -m unittest test_ai_train_core -v
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

from ai_train_core import dataset, paths, progress_events
from ai_train_core.model_config import MODEL_CONFIGS, get_model_config
from ai_train_core.prompt_format import extract_model_response, format_inference_prompt, format_instruction

REQUIRED_KEYS = ["model_name", "display_name", "backend", "total_params", "trainable_params_r16",
                 "trainable_percent_r16", "recommended_gpu", "default_gpu", "min_gpu_memory",
                 "recommended_batch_size", "recommended_grad_accumulation"]


class ModelConfigTest(unittest.TestCase):
    def test_required_keys_and_effective_batch(self):
        for size, cfg in MODEL_CONFIGS.items():
            for key in REQUIRED_KEYS:
                self.assertIn(key, cfg, f"{size} missing {key}")
            self.assertEqual(cfg["recommended_batch_size"] * cfg["recommended_grad_accumulation"], 8)

    def test_backends(self):
        self.assertEqual(get_model_config("4b")["backend"], "unsloth")
        self.assertEqual(get_model_config("12B")["backend"], "transformers")

    def test_invalid_size(self):
        with self.assertRaises(ValueError):
            get_model_config("invalid")


class PromptFormatTest(unittest.TestCase):
    def test_with_and_without_input(self):
        self.assertEqual(
            format_instruction({"instruction": "A", "input": "", "output": "B"})["text"],
            "<start_of_turn>user\nA<end_of_turn>\n<start_of_turn>model\nB<end_of_turn>")
        self.assertEqual(
            format_instruction({"instruction": "A", "input": "C", "output": "B"})["text"],
            "<start_of_turn>user\nA\n\nC<end_of_turn>\n<start_of_turn>model\nB<end_of_turn>")

    def test_inference_prompt_matches_training_prefix(self):
        text = format_instruction({"instruction": "A", "input": "", "output": "B"})["text"]
        self.assertTrue(text.startswith(format_inference_prompt("A")))

    def test_extract_response(self):
        self.assertEqual(extract_model_response("user\nA\n<start_of_turn>model\n hi "), "hi")


class DatasetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.data = str(Path(self.tmp.name) / "d.jsonl")
        self.meta = str(Path(self.tmp.name) / ".metadata.json")
        self.addCleanup(self.tmp.cleanup)

    def test_sync_only_when_newer(self):
        commits = []
        content = b'{"instruction":"a","input":"","output":"b"}\n'
        kw = dict(commit_fn=commits.append, dataset_path=self.data, metadata_path=self.meta)
        self.assertTrue(dataset.sync_dataset(content, 10.0, **kw)["uploaded"])
        self.assertFalse(dataset.sync_dataset(content, 10.0, **kw)["uploaded"])
        self.assertTrue(dataset.sync_dataset(content, 11.0, **kw)["uploaded"])
        self.assertEqual(commits, [[paths.DATA_DIR]] * 2)

    def test_validate(self):
        Path(self.data).write_text('{"instruction":"a","input":"","output":"bb"}\n')
        self.assertEqual(dataset.validate_dataset(self.data)["num_examples"], 1)
        Path(self.data).write_text('{"instruction":"a"}\n')
        with self.assertRaises(ValueError):
            dataset.validate_dataset(self.data)

    def test_bundled_dataset_is_valid(self):
        bundled = Path(__file__).parent / "finetune_data" / paths.DATASET_FILENAME
        if bundled.exists():
            self.assertGreater(dataset.validate_dataset(str(bundled))["num_examples"], 0)


class DatasetNameTest(unittest.TestCase):
    def test_resolve(self):
        self.assertEqual(dataset.resolve_dataset_path(""), paths.REMOTE_DATASET_PATH)
        self.assertEqual(dataset.resolve_dataset_path("my-set_1.jsonl"), f"{paths.DATA_DIR}/my-set_1.jsonl")
        for bad in ("../x.jsonl", "a/b.jsonl", ".metadata.json", ".hidden.jsonl", "x.json", "a..b.jsonl", "x.jsonl\n"):
            self.assertFalse(dataset.valid_dataset_filename(bad), bad)
            with self.assertRaises(ValueError):
                dataset.resolve_dataset_path(bad)


class ProgressEventsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = str(Path(self.tmp.name) / "p.jsonl")

    def test_write_and_read(self):
        commits = []
        progress_events.write_status(self.path, "running", commits.append)
        progress_events.write_event(self.path, {"step": 5, "loss": float("nan"), "learning_rate": 1e-4}, commits.append)
        progress_events.write_status(self.path, "completed", commits.append, final_loss=0.5)
        self.assertEqual(len(commits), 3)
        evs = progress_events.read_events(self.path)
        self.assertEqual([e["type"] for e in evs], ["status", "progress", "status"])
        self.assertIsNone(evs[1]["loss"])  # NaN is not valid JSON: sent as null
        self.assertEqual(evs[1]["step"], 5)
        self.assertFalse(progress_events.is_terminal(evs[0]))
        self.assertTrue(progress_events.is_terminal(evs[2]))

    def test_read_skips_partial_and_garbage_lines(self):
        Path(self.path).write_text('{"step": 1}\nnot json\n\n{"type":"status","status":"failed"}\n{"step": 2')
        evs = progress_events.read_events(self.path)
        self.assertEqual([e["type"] for e in evs], ["progress", "status"])  # old lines have no type: progress
        self.assertEqual(progress_events.read_events(str(Path(self.tmp.name) / "missing")), [])

    def test_job_id(self):
        self.assertTrue(progress_events.valid_job_id("ab12-_"))
        for bad in ("", "../x", "a/b", "a b", "x" * 65):
            self.assertFalse(progress_events.valid_job_id(bad), bad)
            with self.assertRaises(ValueError):
                progress_events.progress_path(bad)


class RunTrainTest(unittest.TestCase):
    """bootstrap.run_train reports the job's life to its progress file (the trainers themselves are faked)."""

    def setUp(self):
        from ai_train_core import bootstrap
        self.bootstrap = bootstrap
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        p = mock.patch("ai_train_core.progress_events.PROGRESS_FILE_TEMPLATE", self.tmp.name + "/p_{timestamp}.jsonl")
        p.start()
        self.addCleanup(p.stop)

    def statuses(self, job_id):
        return [(e["status"], e) for e in progress_events.read_events(f"{self.tmp.name}/p_{job_id}.jsonl")]

    def test_completed(self):
        fake = mock.Mock()
        fake.run_finetuning.return_value = {"status": "success", "final_loss": 0.25}
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}), \
                mock.patch("ai_train_core.bootstrap.get_model_config", return_value={"backend": "unsloth"}):
            self.bootstrap.run_train(3, "4b", dataset="mine.jsonl", job_id="jc")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["dataset_path"], f"{paths.DATA_DIR}/mine.jsonl")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["job_id"], "jc")
        st = self.statuses("jc")
        self.assertEqual([s for s, _ in st], ["running", "completed"])
        self.assertEqual(st[1][1]["final_loss"], 0.25)

    def test_failed(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = RuntimeError("out of memory")
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}), \
                mock.patch("ai_train_core.bootstrap.get_model_config", return_value={"backend": "unsloth"}):
            with self.assertRaises(RuntimeError):
                self.bootstrap.run_train(3, "4b", job_id="jf")
        st = self.statuses("jf")
        self.assertEqual([s for s, _ in st], ["running", "failed"])
        self.assertIn("out of memory", st[1][1]["error"])

    def test_bad_dataset_fails_the_job(self):
        with self.assertRaises(ValueError):
            self.bootstrap.run_train(3, "4b", dataset="../x.jsonl", job_id="jb")
        self.assertEqual([s for s, _ in self.statuses("jb")], ["running", "failed"])


class ApiTest(unittest.TestCase):
    def setUp(self):
        try:
            from fastapi.testclient import TestClient
            from api.api import create_api
        except ImportError:
            self.skipTest("fastapi/httpx not installed")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.data, self.progress = Path(self.tmp.name, "data"), Path(self.tmp.name, "progress")
        self.data.mkdir()
        self.progress.mkdir()
        for target, value in (("api.api.DATA_DIR", str(self.data)), ("api.api.PROGRESS_DIR", str(self.progress)),
                              ("api.api.REMOTE_METADATA_PATH", str(self.data / ".metadata.json")),
                              ("ai_train_core.progress_events.PROGRESS_FILE_TEMPLATE",
                               str(self.progress) + "/training_progress_{timestamp}.jsonl")):
            p = mock.patch(target, value)
            p.start()
            self.addCleanup(p.stop)
        self.calls = []
        api = create_api(
            spawn_training_job=lambda steps, size, ds, job_id: self.calls.append((steps, size, ds, job_id)) or "fc-1",
            generate_response=lambda *a: {"response": "ok", "args": list(a)},
            commit_volumes=lambda p: None,
            reload_volumes=lambda p: None,
            poll_interval_s=0.01,
            max_stream_s=0.3,
        )
        self.client = TestClient(api)

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
        r = self.client.post("/training/job_start", json={"num_train_steps": 7, "model_size": "12b"})
        job = r.json()
        self.assertEqual((job["provider_job_id"], job["status"], job["model_size"], job["num_train_steps"], job["dataset"]),
                         ("fc-1", "started", "12b", 7, ""))
        started = datetime.fromisoformat(job["started_at"].replace("Z", "+00:00"))
        self.assertIsNotNone(started.tzinfo)  # golang needs a timezone to parse it
        self.assertTrue(progress_events.valid_job_id(job["job_id"]))
        self.assertEqual(self.calls, [(7, "12b", "", job["job_id"])])  # the job id names the progress file

    def test_job_start_dataset(self):
        (self.data / "mine.jsonl").write_text("{}\n")
        job = self.client.post("/training/job_start", json={"dataset": "mine.jsonl"}).json()
        self.assertEqual(job["dataset"], "mine.jsonl")
        self.assertEqual(self.calls[0][2], "mine.jsonl")
        n = len(self.calls)
        for bad in ("missing.jsonl", "../mine.jsonl", "mine.txt"):
            self.assertEqual(self.client.post("/training/job_start", json={"dataset": bad}).status_code, 400, bad)
        self.assertEqual(len(self.calls), n)  # nothing was spawned

    def test_upload_and_list_datasets(self):
        r = self.client.post("/datasets/upload", json={"filename": "a.jsonl", "content": base64.b64encode(b"x\n").decode()})
        self.assertEqual(r.status_code, 200)
        self.assertEqual((self.data / "a.jsonl").read_bytes(), b"x\n")  # stored under its own name
        r = self.client.post("/datasets/upload", json={"filename": "b.jsonl", "content": "y\n"})
        self.assertEqual(r.status_code, 200)
        names = sorted(d["filename"] for d in self.client.get("/datasets/list").json()["datasets"])
        self.assertEqual(names, ["a.jsonl", "b.jsonl"])
        self.assertEqual(self.client.post("/datasets/upload", json={"filename": "../x.jsonl", "content": "y"}).status_code, 400)

    def test_job_events_stream(self):
        path = progress_events.progress_path("j1")
        progress_events.write_status(path, "running")
        progress_events.write_event(path, {"type": "progress", "step": 5, "loss": 1.5})
        progress_events.write_status(path, "completed", final_loss=1.2)
        evs = self.events("j1")
        self.assertEqual([(i, e) for i, e, _ in evs], [(0, "status"), (1, "progress"), (2, "status")])
        self.assertEqual(evs[1][2]["loss"], 1.5)
        self.assertEqual(evs[2][2]["final_loss"], 1.2)
        # Resuming: only the events from the given index on.
        self.assertEqual([i for i, _, _ in self.events("j1", "&from=2")], [2])
        self.assertEqual(self.events("j1", "&from=3"), [])  # past the end of a finished job: ends after max_stream_s

    def test_job_events_follows_a_running_job(self):
        path = progress_events.progress_path("j2")
        self.assertEqual(self.events("j2"), [])  # no file yet: waits, then gives up when max_stream_s is over
        progress_events.write_status(path, "running")
        self.assertEqual([e["status"] for _, _, e in self.events("j2")], ["running"])  # not finished: stream times out
        self.assertEqual(self.client.get("/training/job_events").status_code, 400)
        self.assertEqual(self.client.get("/training/job_events?job_id=../x").status_code, 400)

    def test_job_progress(self):
        self.assertEqual(self.client.get("/training/job_progress").json(), {"progress": []})
        progress_events.write_event(progress_events.progress_path("a"), {"step": 1})
        progress_events.write_event(progress_events.progress_path("b"), {"step": 2})
        os.utime(progress_events.progress_path("a"), (1, 1))
        self.assertEqual(self.client.get("/training/job_progress").json()["progress"][0]["step"], 2)  # newest
        self.assertEqual(self.client.get("/training/job_progress?job_id=a").json()["progress"][0]["step"], 1)

    def test_job_start_defaults_and_invalid_steps(self):
        job = self.client.post("/training/job_start", json={}).json()
        self.assertEqual((job["model_size"], job["num_train_steps"]), ("4b", 100))
        for bad in (0, -1, "5", True):
            self.assertEqual(self.client.post("/training/job_start", json={"num_train_steps": bad}).status_code, 400)

    def test_invalid_model_size(self):
        self.assertEqual(self.client.post("/training/job_start", json={"model_size": "99b"}).status_code, 400)
        self.assertEqual(self.client.post("/infer/query", json={"model_size": "99b"}).status_code, 400)

    def test_generate(self):
        r = self.client.post("/infer/query", json={"prompt": "hi"})
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "4b"])


if __name__ == "__main__":
    unittest.main()
