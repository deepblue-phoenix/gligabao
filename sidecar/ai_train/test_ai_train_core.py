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

from ai_train_core import dataset, paths, progress_events, trained_model
from ai_train_core.model_config import MODEL_CONFIGS, get_model_config
from ai_train_core.prompt_format import extract_model_response, format_inference_prompt, format_instruction

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

    def test_log_and_cancelled_events(self):
        progress_events.write_log(self.path, "loading the model")
        progress_events.write_status(self.path, "cancelled", error="stopped at step 3")
        evs = progress_events.read_events(self.path)
        self.assertEqual((evs[0]["type"], evs[0]["message"]), ("log", "loading the model"))
        self.assertFalse(progress_events.is_terminal(evs[0]))
        self.assertTrue(progress_events.is_terminal(evs[1]))

    def test_stop_request(self):
        with mock.patch("ai_train_core.progress_events.PROGRESS_FILE_TEMPLATE", self.tmp.name + "/p_{timestamp}.jsonl"):
            commits, reloads = [], []
            self.assertFalse(progress_events.stop_requested("s1", reloads.append))
            progress_events.request_stop("s1", commits.append)
            self.assertEqual(len(commits), 1)
            self.assertTrue(progress_events.stop_requested("s1", reloads.append))
            self.assertEqual(len(reloads), 2)  # the volume is refreshed before each look
            self.assertFalse(progress_events.stop_requested("s2"))  # per job
            self.assertNotEqual(progress_events.stop_path("s1"), progress_events.progress_path("s1"))

    def test_job_id(self):
        self.assertTrue(progress_events.valid_job_id("ab12-_"))
        for bad in ("", "../x", "a/b", "a b", "x" * 65):
            self.assertFalse(progress_events.valid_job_id(bad), bad)
            with self.assertRaises(ValueError):
                progress_events.progress_path(bad)


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
        return [(e["status"], e) for e in progress_events.read_events(f"{self.tmp.name}/p_{job_id}.jsonl")
                if e["type"] == "status"]

    def test_completed(self):
        fake = mock.Mock()
        model = {"path": "trained/jc", "files": [], "size_bytes": 0}
        fake.run_finetuning.return_value = {"status": "success", "final_loss": 0.25, "model": model}
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}):
            self.bootstrap.run_train(3, "4b", dataset="mine.jsonl", job_id="jc")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["dataset_path"], f"{paths.DATA_DIR}/mine.jsonl")
        self.assertEqual(fake.run_finetuning.call_args.kwargs["job_id"], "jc")
        st = self.statuses("jc")
        self.assertEqual([s for s, _ in st], ["running", "completed"])
        self.assertEqual(st[1][1]["final_loss"], 0.25)
        self.assertEqual(st[1][1]["model"], model)  # the weights the job saved ride on the completed event

    def test_failed(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = RuntimeError("out of memory")
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}):
            with self.assertRaises(RuntimeError):
                self.bootstrap.run_train(3, "4b", job_id="jf")
        st = self.statuses("jf")
        self.assertEqual([s for s, _ in st], ["running", "failed"])
        self.assertIn("out of memory", st[1][1]["error"])

    def test_cancelled(self):
        fake = mock.Mock()
        fake.run_finetuning.side_effect = progress_events.JobCancelled("stopped at step 4")
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}):
            result = self.bootstrap.run_train(3, "4b", job_id="jx")  # not an error: nothing is raised
        self.assertEqual(result["status"], "cancelled")
        st = self.statuses("jx")
        self.assertEqual([s for s, _ in st], ["running", "cancelled"])
        self.assertIn("step 4", st[-1][1]["error"])

    def test_reload_fn_reaches_the_trainer(self):
        fake = mock.Mock()
        fake.run_finetuning.return_value = {"status": "success", "final_loss": 0.1}
        reload_fn = mock.Mock()
        with mock.patch.dict(sys.modules, {"ai_train_core.model_train": fake}):
            self.bootstrap.run_train(3, "4b", job_id="jr", reload_fn=reload_fn)
        self.assertIs(fake.run_finetuning.call_args.kwargs["reload_fn"], reload_fn)

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
                              ("ai_train_core.progress_events.PROGRESS_FILE_TEMPLATE",
                               str(self.progress) + "/training_progress_{timestamp}.jsonl")):
            p = mock.patch(target, value)
            p.start()
            self.addCleanup(p.stop)
        self.calls, self.commits = [], []
        api = create_api(
            spawn_training_job=lambda steps, size, ds, job_id: self.calls.append((steps, size, ds, job_id)) or "fc-1",
            generate_response=lambda *a: {"response": "ok", "args": list(a)},
            commit_volumes=self.commits.append,
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

    def test_job_start_stores_inline_dataset(self):
        content = base64.b64encode(b'{"instruction":"a","output":"b"}\n').decode()
        job = self.client.post("/training/job_start", json={"dataset": "new.jsonl", "dataset_content": content}).json()
        self.assertEqual(job["dataset"], "new.jsonl")
        self.assertEqual((self.data / "new.jsonl").read_bytes(), b'{"instruction":"a","output":"b"}\n')
        self.assertEqual(self.commits, [[str(self.data)]])
        n = len(self.calls)
        for body in ({"dataset": "x.jsonl", "dataset_content": "not base64!"},
                     {"dataset": "../x.jsonl", "dataset_content": content},
                     {"dataset_content": content}):
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

    def test_job_cancel(self):
        path = progress_events.progress_path("c1")
        post = lambda body: self.client.post("/training/job_cancel", json=body)
        self.assertEqual(post({}).status_code, 400)
        self.assertEqual(post({"job_id": "../x"}).status_code, 400)
        self.assertEqual(post({"job_id": "c1"}).status_code, 404)  # no progress file: unknown job
        progress_events.write_status(path, "training", max_steps=10)
        r = post({"job_id": "c1"})
        self.assertEqual((r.status_code, r.json()), (200, {"job_id": "c1", "status": "stopping"}))
        self.assertTrue(progress_events.stop_requested("c1"))
        self.assertEqual(self.commits[-1], ["/progress"])  # the stop file is committed for the training container
        # The web container never writes to the progress file itself.
        self.assertEqual(len(progress_events.read_events(path)), 1)
        progress_events.write_status(path, "cancelled")
        self.assertEqual(post({"job_id": "c1"}).status_code, 409)  # over already

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
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "4b", ""])

    def test_generate_with_a_trained_model(self):
        r = self.client.post("/infer/query", json={"prompt": "hi", "model_size": "12b", "model_path": "trained/abc123"})
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "12b", "trained/abc123"])
        for bad in ("../x", "trained/../x", "/models/trained/a", "finetuned_model_4b", "trained/a/b", "trained/"):
            r = self.client.post("/infer/query", json={"prompt": "hi", "model_path": bad})
            self.assertEqual(r.status_code, 400, bad)


if __name__ == "__main__":
    unittest.main()
