"""Local (client-side) training datasets, for the `modal run app.py` CLI.

A dataset is a subdirectory of the training datasets directory (`bin/datasets_training/` of the gligabao repo) with a `dataset.toml`
manifest that names it and says where its JSONL data file is, relative to the manifest. This reads the manifests the way the
Go server does (internal/training/manifest.go; the keys are in docs/training/DATASETS.md), so a dataset the server
lists can be uploaded from here too. Needs Python 3.11+ (tomllib).
"""
import os
import re
import tomllib
from dataclasses import dataclass
from pathlib import Path

modd_str = os.path.abspath(os.path.dirname(__file__))  # module dir

MANIFEST_FILENAME = "dataset.toml"
# bin/datasets_training of the gligabao repo (this directory is sidecar/ai_train); GLIGABAO_DATASETS_TRAINING_DIR overrides it.
DEFAULT_DATASETS_DIR = Path(modd_str).parent.parent / "bin" / "datasets_training"
_NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,121}")


class DatasetError(Exception):
    """A dataset that is unknown or cannot be used; the message is meant for the user."""


@dataclass(frozen=True)
class LocalDataset:
    name: str
    description: str
    path: Path          # the data file

    @property
    def filename(self) -> str:
        """The file name the dataset has on the data volume (and in a training job's `dataset`)."""
        return f"{self.name}.jsonl"


def datasets_dir() -> Path:
    return Path(os.environ.get("GLIGABAO_DATASETS_TRAINING_DIR") or DEFAULT_DATASETS_DIR)


def _read_manifest(manifest: Path) -> LocalDataset:
    """The dataset a manifest describes. Raises DatasetError if it is not usable (same rules as the Go server)."""
    try:
        with open(manifest, "rb") as f:
            m = tomllib.load(f)
    except (tomllib.TOMLDecodeError, OSError) as e:
        raise DatasetError(f"invalid manifest: {e}") from None
    name = m.get("name") or manifest.parent.name
    if not isinstance(name, str) or not _NAME_RE.fullmatch(name) or ".." in name:
        raise DatasetError(f"invalid dataset name {name!r}: set `name` in the manifest")
    if (m.get("format") or "jsonl") != "jsonl":
        raise DatasetError(f"unsupported format {m.get('format')!r} (only 'jsonl')")
    file = m.get("file")
    if not isinstance(file, str) or not file:
        raise DatasetError("the manifest has no `file` (the data file, relative to the manifest)")
    rel = Path(file)
    if rel.is_absolute() or ".." in rel.parts or rel.suffix != ".jsonl":
        raise DatasetError(f"`file` {file!r} must be a relative path inside the dataset's directory, ending in .jsonl")
    path = manifest.parent / rel
    if not path.is_file():
        hint = f": run {m['generator']} in {manifest.parent} to create it" if m.get("generator") else ""
        raise DatasetError(f"the data file {file} is missing{hint}")
    return LocalDataset(name=name, description=str(m.get("description") or ""), path=path)


def list_local_datasets(root: Path = None) -> tuple:
    """(usable datasets sorted by name, problems as "<dir>: <message>" strings) of the training datasets directory."""
    root = Path(root) if root else datasets_dir()
    found, problems, taken = [], [], set()
    for sub in sorted(root.iterdir()) if root.is_dir() else []:
        manifest = sub / MANIFEST_FILENAME
        if sub.name.startswith(".") or not manifest.is_file():
            continue
        try:
            ds = _read_manifest(manifest)
            if ds.name in taken:
                raise DatasetError(f"the name {ds.name!r} is already taken by another dataset")
        except DatasetError as e:
            problems.append(f"{sub.name}: {e}")
            continue
        taken.add(ds.name)
        found.append(ds)
    return sorted(found, key=lambda d: d.name), problems


def find_local_dataset(name: str, root: Path = None) -> LocalDataset:
    """The local dataset called `name`. Raises DatasetError if there is none."""
    found, problems = list_local_datasets(root)
    for ds in found:
        if ds.name == name:
            return ds
    detail = "".join(f"\n  not usable: {p}" for p in problems)
    raise DatasetError(f"no dataset named {name!r} in {Path(root) if root else datasets_dir()}" + detail)
