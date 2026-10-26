"""The gligabao server: a prebuilt Go binary and its default config, installed by `pip install gligabao-server`.

    gligabao-server serve            # same as the `gligabao serve` of the binary; `gligabao` works too
    gligabao-server serve --config my.toml --db data/gligabao.db

From Python:

    import subprocess, gligabao_server
    subprocess.Popen([gligabao_server.binary_path(), "serve"])
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

from ._version import __version__

__all__ = ["__version__", "binary_path", "config_path", "main"]

_BIN_DIR = Path(__file__).resolve().parent / "_bin"


def binary_path() -> str:
    """Path of the bundled gligabao binary. Raises FileNotFoundError if this install has none (for example a
    source checkout that was not built with run_pypi.sh)."""
    name = "gligabao.exe" if sys.platform == "win32" else "gligabao"
    path = _BIN_DIR / name
    if not path.is_file():
        raise FileNotFoundError(f"the gligabao binary is not in {_BIN_DIR}: install a gligabao-server wheel for your platform")
    return str(path)


def config_path() -> str:
    """Path of the default gligabao.toml, which sits next to the binary (the binary reads it when --config is not given).
    It is inside site-packages and is replaced when the package is upgraded: for your own settings keep a copy
    elsewhere and pass `--config <path>`."""
    return str(_BIN_DIR / "gligabao.toml")


def main() -> None:
    """Entry point of the `gligabao-server` and `gligabao` commands: runs the binary with the same arguments."""
    try:
        binary = binary_path()
    except FileNotFoundError as e:
        sys.exit(str(e))
    if sys.platform == "win32":
        sys.exit(subprocess.call([binary, *sys.argv[1:]]))  # os.execv does not replace the process on Windows
    else:
        try:
            os.chmod(binary, os.stat(binary).st_mode | 0o111)  # in case an installer dropped the executable bit
        except OSError:
            pass
        os.execv(binary, [binary, *sys.argv[1:]])
