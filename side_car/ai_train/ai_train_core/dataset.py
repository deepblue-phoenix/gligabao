"""Dataset sync/validation on the data volume. Provider neutral; runs inside the container."""
import json
import re
from datetime import datetime
from pathlib import Path
from typing import Callable, Optional

from .paths import DATA_DIR, REMOTE_DATASET_PATH, REMOTE_METADATA_PATH

# Datasets are .jsonl files directly in DATA_DIR. A name never has a path or a leading dot (".metadata.json" lives there).
_DATASET_NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.jsonl")


def valid_dataset_filename(name: str) -> bool:
    return isinstance(name, str) and ".." not in name and _DATASET_NAME_RE.fullmatch(name) is not None


def resolve_dataset_path(name: str = "") -> str:
    """Path on the data volume of the dataset `name` (the default dataset if empty). Raises ValueError for a bad name."""
    if not name:
        return REMOTE_DATASET_PATH
    if not valid_dataset_filename(name):
        raise ValueError(f"Invalid dataset name {name!r}: must be a plain *.jsonl file name")
    return f"{DATA_DIR}/{name}"


def sync_dataset(content: bytes, local_mtime: float, commit_fn: Optional[Callable] = None,
                 dataset_path: str = REMOTE_DATASET_PATH, metadata_path: str = REMOTE_METADATA_PATH) -> dict:
    """Write the dataset to the data volume if the local copy is newer than the remote one."""
    remote, meta = Path(dataset_path), Path(metadata_path)

    if remote.exists() and meta.exists():
        try:
            remote_mtime = json.loads(meta.read_text()).get("mtime", 0)
            if remote_mtime >= local_mtime:
                print(f"Remote dataset is up to date (mtime: {remote_mtime})")
                return {"uploaded": False, "message": "Remote dataset is current"}
            print(f"Local dataset is newer ({local_mtime} > {remote_mtime})")
        except Exception as e:  # unreadable metadata: just re-upload
            print(f"Error reading metadata: {e}")
    else:
        print("No remote dataset found, uploading...")

    remote.parent.mkdir(parents=True, exist_ok=True)
    remote.write_bytes(content)
    meta.write_text(json.dumps({
        "filename": remote.name,
        "mtime": local_mtime,
        "uploaded_at": datetime.now().isoformat(),
        "size_bytes": len(content),
    }, indent=2))
    if commit_fn:
        commit_fn([DATA_DIR])
    print(f"Dataset uploaded to volume ({len(content)} bytes)")
    return {"uploaded": True, "size": len(content)}


def validate_dataset(dataset_path: str = REMOTE_DATASET_PATH) -> dict:
    """Check every line has instruction/input/output. Returns summary stats."""
    path = Path(dataset_path)
    if not path.exists():
        raise FileNotFoundError(f"Dataset not found at {dataset_path}. Run the 'sync' action first.")

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
