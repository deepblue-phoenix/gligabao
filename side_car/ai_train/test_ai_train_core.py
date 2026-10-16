"""Local unit tests for the provider-neutral training code. No GPU, Modal or ML libs needed.

    cd side_car/ai_train && python -m unittest test_ai_train_core -v
"""
import base64
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ai_train_core import dataset, paths
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


class ApiTest(unittest.TestCase):
    def setUp(self):
        try:
            from fastapi.testclient import TestClient
            from api.api import create_api
        except ImportError:
            self.skipTest("fastapi/httpx not installed")
        self.calls = []
        api = create_api(
            spawn_training_job=lambda steps, size: self.calls.append((steps, size)) or "job-1",
            generate_response=lambda *a: {"response": "ok", "args": list(a)},
            commit_volumes=lambda p: None,
            reload_volumes=lambda p: None,
        )
        self.client = TestClient(api)

    def test_healthz(self):
        self.assertEqual(self.client.get("/healthz").json()["status"], "ok")

    def test_start_training_job(self):
        r = self.client.post("/start_training_job", json={"num_train_steps": 7, "model_size": "12b"})
        self.assertEqual(r.json()["job_id"], "job-1")
        self.assertEqual(self.calls, [(7, "12b")])

    def test_invalid_model_size(self):
        self.assertEqual(self.client.post("/start_training_job", json={"model_size": "99b"}).status_code, 400)
        self.assertEqual(self.client.post("/generate_response", json={"model_size": "99b"}).status_code, 400)

    def test_generate(self):
        r = self.client.post("/generate_response", json={"prompt": "hi"})
        self.assertEqual(r.json()["args"], ["hi", 1024, 0.7, "4b"])


if __name__ == "__main__":
    unittest.main()
