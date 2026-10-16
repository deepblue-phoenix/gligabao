"""Code that runs INSIDE the remote container. Provider neutral: no modal/aws imports.

Keep this the place where real training grows; app.py only wires it up.
"""
import json
import os
import platform
import shutil
import subprocess
import time
from pathlib import Path


def run_ping(volume_paths: list) -> dict:
    """Report what the container looks like. Used to verify the bootstrap end to end."""
    gpu = None
    if shutil.which("nvidia-smi"):
        r = subprocess.run(["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"],
                           capture_output=True, text=True)
        gpu = r.stdout.strip() or r.stderr.strip()
    return {
        "python": platform.python_version(),
        "gpu": gpu,
        "volumes": {p: os.path.isdir(p) for p in volume_paths},
        "hf_token_set": bool(os.environ.get("HF_TOKEN")),
    }


def run_train(num_train_steps: int, progress_dir: str = "/progress") -> dict:
    """Stub training job: writes a progress file so volumes/commit can be tested. Real training TBD."""
    out = Path(progress_dir)
    out.mkdir(parents=True, exist_ok=True)
    path = out / f"training_progress_{int(time.time())}.jsonl"
    with open(path, "w") as f:
        for step in range(1, num_train_steps + 1):
            f.write(json.dumps({"step": step, "loss": None, "ts": time.time()}) + "\n")
    return {"status": "stub", "num_steps": num_train_steps, "progress_file": str(path)}
