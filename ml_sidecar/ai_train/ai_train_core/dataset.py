"""Dataset sync/validation on the data volume. Provider neutral; runs inside the container."""
import json
import re
from pathlib import Path
from typing import Callable, Optional

from .paths import DATA_DIR

# Datasets are .jsonl files directly in DATA_DIR. A name never has a path or a leading dot.
_DATASET_NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.jsonl")


def valid_dataset_filename(name: str) -> bool:
    return isinstance(name, str) and ".." not in name and _DATASET_NAME_RE.fullmatch(name) is not None


def resolve_dataset_path(name: str) -> str:
    """Path on the data volume of the dataset file `name` (`<dataset name>.jsonl`). Raises ValueError for a bad or empty name."""
    if not name:
        raise ValueError("No dataset: a training job needs one (the file name on the data volume, <dataset name>.jsonl)")
    if not valid_dataset_filename(name):
        raise ValueError(f"Invalid dataset name {name!r}: must be a plain *.jsonl file name")
    return f"{DATA_DIR}/{name}"


def store_dataset(name: str, content: bytes, commit_fn: Optional[Callable] = None, data_dir: str = DATA_DIR) -> None:
    """Save a dataset on the data volume under its own name (replacing any earlier one) and commit the volume.
    Internal: datasets have no public endpoint. Raises ValueError for a bad name."""
    if not valid_dataset_filename(name):
        raise ValueError(f"Invalid dataset name {name!r}: must be a plain *.jsonl file name")
    path = Path(data_dir) / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)
    if commit_fn:
        commit_fn([data_dir])


def sync_dataset(content: bytes, name: str, commit_fn: Optional[Callable] = None, data_dir: str = DATA_DIR) -> dict:
    """Write the dataset file `name` to the data volume unless the volume already has exactly this content."""
    path = Path(resolve_dataset_path(name) if data_dir == DATA_DIR else f"{data_dir}/{name}")
    if path.is_file() and path.read_bytes() == content:
        print(f"Remote dataset {name} is up to date")
        return {"uploaded": False, "message": "Remote dataset is current"}
    store_dataset(name, content, commit_fn, data_dir)
    print(f"Dataset {name} uploaded to volume ({len(content)} bytes)")
    return {"uploaded": True, "size": len(content)}


def validate_dataset(dataset_path: str) -> dict:
    """Check every line has instruction/input/output. Returns summary stats."""
    path = Path(dataset_path)
    if not path.exists():
        raise FileNotFoundError(f"Dataset not found at {dataset_path}. Run the 'sync' action for it first.")

    n = instr_chars = out_chars = 0
    with open(path) as f:
        for i, line in enumerate(f, 1):
            if not line.strip():
                continue
            try:
                ex = json.loads(line)
                assert "instruction" in ex and "input" in ex and "output" in ex, "missing instruction/input/output"
            except (json.JSONDecodeError, AssertionError) as e:
                raise ValueError(f"Invalid example at line {i}: {e}")
            n += 1
            instr_chars += len(ex["instruction"])
            out_chars += len(ex["output"])
    if n == 0:
        raise ValueError(f"Dataset {dataset_path} is empty")

    stats = {"num_examples": n, "avg_instruction_chars": round(instr_chars / n),
             "avg_output_chars": round(out_chars / n)}
    print(f"Validated {stats}")
    return stats
