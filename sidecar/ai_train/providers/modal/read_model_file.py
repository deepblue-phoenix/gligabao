"""Writes one file of a trained model, read from the models volume, to stdout (raw bytes).

    python -m providers.modal.read_model_file <model_path> <file>      e.g. trained/<job id> adapter_model.safetensors

`model_path` is the path of a trained model on the models volume (`trained/<job id>`), `file` a file in its directory (the
`path` of an entry of the model's `files`). Run by the sidecar (backends/modal_backend.py) with `sidecar/ai_train/` as cwd and
the Modal credentials in the environment, so the Go server can fetch a model's files through the sidecar. Read only.

Exit code: 0 done, 2 invalid `model_path` or `file`, 3 the volume or the file does not exist, 1 any other failure (the last
line of stderr says why).
"""
import sys

import modal
import modal.exception

from ai_train_core import paths
from ai_train_core.trained_model import valid_model_file, valid_model_path
from config import CONFIG

EXIT_INVALID = 2
EXIT_NOT_FOUND = 3


def main(argv: list) -> int:
    if len(argv) != 3 or not valid_model_path(argv[1]) or not valid_model_file(argv[2]):
        print(f"invalid model path or file: expected 'trained/<job id>' and a relative file path, got {argv[1:]!r}", file=sys.stderr)
        return EXIT_INVALID
    name = CONFIG.volumes[paths.MODELS_DIR]
    try:
        vol = modal.Volume.from_name(name).hydrate()
    except modal.exception.NotFoundError:
        print(f"the models volume {name!r} does not exist", file=sys.stderr)
        return EXIT_NOT_FOUND
    out = sys.stdout.buffer
    try:
        for chunk in vol.read_file(f"{argv[1]}/{argv[2]}"):
            out.write(chunk)
    except (FileNotFoundError, modal.exception.NotFoundError):
        print(f"no file {argv[2]!r} in {argv[1]!r} on the models volume {name!r}", file=sys.stderr)
        return EXIT_NOT_FOUND
    out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
