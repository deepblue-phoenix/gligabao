#!/usr/bin/env python3
"""Builds and uploads the two PyPI packages of gligabao. See docs/pypi.md.

  gligabao         clients/py          the Python client (pure Python, one wheel + sdist)
  gligabao-server  clients/py_server   the server: one wheel per platform, each with the Go binary + gligabao.toml

REQUIREMENT: like ops/build.py, this program must run natively on Linux, macOS and Windows (standard library only, no
shell, no sh/cp/rm). The shell script run_pypi.sh at the repo root is only a wrapper for Linux/macOS.

Building and uploading are independent: building writes wheels to the dist/ directory of each package, --upload
uploads what is in those directories (the current version of each package only). Combine them in one call to do both.

The server wheels have one build function per platform (build_server_linux, build_server_macos, build_server_windows).
The Go binary cross-compiles, so any host can build the wheels of any platform; the per-platform functions hold what is
specific to each one (its targets, wheel platform tags, executable name and whether sargo is included), and --build_server_host builds the wheels of
the platform this program runs on.

sargo (the Rust model router) is embedded into the server binary of a wheel: by default for Linux only (it is built on the
Linux machine, for its CPU architecture), and for macOS / Windows only with an explicit --sargo, because Rust needs a
native toolchain per OS (run the build on that OS). --no-sargo leaves it out everywhere.

Usage: python ops/pypi.py [build flags] [--upload [--test] [--yes]]    (Windows: py ops/pypi.py ...)
"""

from __future__ import annotations

import argparse
import importlib.metadata
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Sequence

# ops/build.py (the Go, npm and cargo build steps). Not to be confused with the PyPI package `build`, which is only run as `python -m build`.
import build as gb_build

ROOT = gb_build.ROOT
CLIENT_DIR = ROOT / "clients" / "py"
SERVER_DIR = ROOT / "clients" / "py_server"
STAGE_ROOT = ROOT / "build" / "pypi"
WHEEL_HOOK = ROOT / "ops" / "wheel_platform_tag_hook.py"  # copied next to pyproject.toml in each staging directory

PY_TOOLS = ("build", "twine", "pytest")  # pip distributions this program runs with `python -m`


# ---------------------------------------------------------------------------------------------
# Data


@dataclass(frozen=True)
class WheelTarget:
    """One server wheel: the Go target and the platform tag of the wheel (see ops/wheel_platform_tag_hook.py)."""

    target: gb_build.Target
    tag: str

    @property
    def stage(self) -> Path:
        return STAGE_ROOT / f"server-{self.target.os}-{self.target.arch}"

    @property
    def exe_name(self) -> str:
        return f"gligabao{self.target.exe}"


@dataclass(frozen=True)
class Options:
    admin_ui: bool = True
    arch: str | None = None  # restrict the server builds to one architecture
    sargo: bool | None = None  # True: --sargo, False: --no-sargo, None: the default of each platform (SARGO_BY_DEFAULT)


# Whether the wheels of a platform include sargo (the Rust model router, embedded in the server binary, `gligabao sargo serve`).
# Linux: yes. macOS and Windows: only with an explicit --sargo, because Rust cannot be cross-compiled: it needs a Mac / Windows machine.
SARGO_BY_DEFAULT = {"linux": True, "darwin": False, "windows": False}


# The Linux binary is static (CGO off), so the same file serves glibc (manylinux) and musl (musllinux) systems.
# The macOS minimum is what the Go toolchain supports. Windows has plain platform tags.
LINUX_TARGETS = (
    WheelTarget(gb_build.Target("linux", "amd64"), "manylinux2014_x86_64.musllinux_1_1_x86_64"),
    WheelTarget(gb_build.Target("linux", "arm64"), "manylinux2014_aarch64.musllinux_1_1_aarch64"),
)
MACOS_TARGETS = (
    WheelTarget(gb_build.Target("darwin", "amd64"), "macosx_12_0_x86_64"),
    WheelTarget(gb_build.Target("darwin", "arm64"), "macosx_12_0_arm64"),
)
WINDOWS_TARGETS = (
    WheelTarget(gb_build.Target("windows", "amd64"), "win_amd64"),
    WheelTarget(gb_build.Target("windows", "arm64"), "win_arm64"),
)


# ---------------------------------------------------------------------------------------------
# Pure helpers


def select_targets(targets: Sequence[WheelTarget],
                   arch: str | None) -> tuple[WheelTarget, ...]:
    return tuple(t for t in targets if arch is None or t.target.arch == arch)


def wants_sargo(target: gb_build.Target,
                opts: Options) -> bool:
    return SARGO_BY_DEFAULT[target.os] if opts.sargo is None else opts.sargo


def sargo_problems(targets: Sequence[WheelTarget],
                   opts: Options) -> list[str]:
    """Why sargo cannot be built for the targets that want it on this machine (checked before anything is built)."""
    reasons = (gb_build.sargo_unsupported_reason(t.target) for t in targets if wants_sargo(t.target, opts))
    return sorted({r for r in reasons if r is not None})


def server_wheel_name(version: str,
                      tag: str) -> str:
    return f"gligabao_server-{version}-py3-none-{tag}.whl"


def read_client_version() -> str:
    text = (CLIENT_DIR / "pyproject.toml").read_text(encoding="utf-8")
    try:
        import tomllib  # Python 3.11+

        return tomllib.loads(text)["project"]["version"]
    except ImportError:
        match = re.search(r'^version\s*=\s*"([^"]+)"', text, re.MULTILINE)
        if match is None:
            raise gb_build.BuildError("cannot read the version of clients/py/pyproject.toml")
        return match.group(1)


def python_env(*extra_paths: Path) -> dict[str, str]:
    """Environment with the given directories on PYTHONPATH (in front of an existing value)."""
    parts = [*(str(p) for p in extra_paths), os.environ.get("PYTHONPATH", "")]
    return {**os.environ, "PYTHONPATH": os.pathsep.join(p for p in parts if p)}


def python_module(module: str,
                  *args: str) -> list[str]:
    return [sys.executable, "-m", module, *args]


# ---------------------------------------------------------------------------------------------
# Side effects


def missing_tools() -> list[str]:
    """pip distributions of PY_TOOLS that are not installed. Asks the package metadata, not `import`, because the
    module name `build` is shadowed by ops/build.py in this process."""
    def installed(name: str) -> bool:
        try:
            importlib.metadata.version(name)
            return True
        except importlib.metadata.PackageNotFoundError:
            return False

    return [name for name in PY_TOOLS if not installed(name)]


def ensure_python_tools() -> None:
    missing = missing_tools()
    if missing:
        gb_build.run(python_module("pip", "install", "--quiet", *missing))


def remove_files(pattern_dir: Path,
                 *patterns: str) -> None:
    for pattern in patterns:
        for path in pattern_dir.glob(pattern):
            path.unlink()


# ---------------------------------------------------------------------------------------------
# Client


def build_client() -> None:
    version = read_client_version()
    gb_build.log(f"Building gligabao (client) {version}")
    gb_build.run(python_module("pytest", "-q"), cwd=CLIENT_DIR, env=python_env(CLIENT_DIR / "src"))
    dist = CLIENT_DIR / "dist"
    dist.mkdir(exist_ok=True)
    # Only this version's files are replaced: wheels of other versions stay in dist/.
    remove_files(dist, f"gligabao-{version}-*.whl", f"gligabao-{version}.tar.gz")
    gb_build.run(python_module("build", "--outdir", "dist"), cwd=CLIENT_DIR)
    gb_build.run(python_module("twine", "check", *(str(p) for p in client_files(version))), cwd=CLIENT_DIR)


def client_files(version: str) -> list[Path]:
    dist = CLIENT_DIR / "dist"
    return sorted([*dist.glob(f"gligabao-{version}-*.whl"), *dist.glob(f"gligabao-{version}.tar.gz")])


# ---------------------------------------------------------------------------------------------
# Server: staging and one wheel (shared), then one function per platform


def stage_server_package(wt: WheelTarget,
                         version: str,
                         with_sargo: bool) -> None:
    """The staging directory: package sources, the wheel hook, the binary for the target (with sargo embedded if asked) and the default config."""
    stage = wt.stage
    shutil.rmtree(stage, ignore_errors=True)
    bin_dir = stage / "src" / "gligabao_server" / "_bin"
    bin_dir.mkdir(parents=True)
    for name in ("pyproject.toml", "README.md"):
        shutil.copy2(SERVER_DIR / name, stage / name)
    shutil.copy2(WHEEL_HOOK, stage / WHEEL_HOOK.name)
    shutil.copytree(SERVER_DIR / "src" / "gligabao_server", stage / "src" / "gligabao_server",
                    ignore=shutil.ignore_patterns("__pycache__", "_bin"), dirs_exist_ok=True)
    (stage / "src" / "gligabao_server" / "_version.py").write_text(f'__version__ = "{version}"\n', encoding="utf-8")

    # sargo is embedded into the Go binary by `go build` (rust/sargo/embed.go), so the stale or wanted binary is put in place first.
    # The sidecar (sidecar/) is not part of the package. The config is the default one the binary finds next to itself.
    if with_sargo:
        gb_build.build_sargo(wt.target, static=True, embed=True)
    else:
        gb_build.drop_embedded_sargo()
    gb_build.go_server_binary(wt.target, bin_dir / wt.exe_name, release=True, version=version)
    gb_build.drop_embedded_sargo()  # leave the tree clean for the next target
    shutil.copy2(ROOT / "build" / "gligabao.toml", bin_dir / "gligabao.toml")


def build_server_wheel(wt: WheelTarget,
                       version: str,
                       with_sargo: bool) -> None:
    gb_build.log(f"Building gligabao-server {version} for {wt.target.os}/{wt.target.arch} ({wt.tag}), "
                 f"sargo {'embedded' if with_sargo else 'not included'}")
    stage_server_package(wt, version, with_sargo)
    dist = SERVER_DIR / "dist"
    dist.mkdir(exist_ok=True)
    wheel = dist / server_wheel_name(version, wt.tag)
    wheel.unlink(missing_ok=True)  # platform tags are dotted lists, so the wheel of this target is found by its full name
    gb_build.run(python_module("build", "--wheel", "--outdir", str(dist), str(wt.stage)),
                 env={**os.environ, "GLIGABAO_WHEEL_TAG": wt.tag})
    gb_build.run(python_module("twine", "check", str(wheel)))


def build_wheels(chosen: Sequence[WheelTarget],
                 opts: Options,
                 version: str) -> int:
    for wt in chosen:
        build_server_wheel(wt, version, wants_sargo(wt.target, opts))
    return len(chosen)


def build_server_linux(opts: Options,
                       version: str) -> int:
    """Linux wheels (amd64, arm64): a static binary, so one file serves glibc and musl; hence the double wheel tag.
    sargo (static musl) is built and embedded by default, for the host architecture only (see sargo_problems)."""
    return build_wheels(select_targets(LINUX_TARGETS, opts.arch), opts, version)


def build_server_macos(opts: Options,
                       version: str) -> int:
    """macOS wheels (amd64, arm64): tagged with the minimum macOS version the Go toolchain supports (12).
    Only the Go server unless --sargo is given (which needs a Mac)."""
    return build_wheels(select_targets(MACOS_TARGETS, opts.arch), opts, version)


def build_server_windows(opts: Options,
                         version: str) -> int:
    """Windows wheels (amd64, arm64): the binary is gligabao.exe, which gligabao_server.binary_path() looks for on win32.
    Only the Go server unless --sargo is given (which needs a Windows machine)."""
    return build_wheels(select_targets(WINDOWS_TARGETS, opts.arch), opts, version)


TARGETS = {"linux": LINUX_TARGETS, "darwin": MACOS_TARGETS, "windows": WINDOWS_TARGETS}

SERVER_BUILDERS: dict[str, Callable[[Options, str], int]] = {
    "linux": build_server_linux,
    "darwin": build_server_macos,
    "windows": build_server_windows,
}


def build_server(os_names: Sequence[str],
                 opts: Options) -> None:
    version = gb_build.read_version()
    problems = sargo_problems([t for name in dict.fromkeys(os_names) for t in TARGETS[name] if opts.arch in (None, t.target.arch)], opts)
    if problems:  # fail before the slow steps
        raise gb_build.BuildError("\n".join([*problems, "Use --arch to build only the targets this machine can, or --no-sargo."]))
    gb_build.run(python_module("pytest", "-q", str(SERVER_DIR / "tests")), env=python_env(SERVER_DIR / "src"))
    gb_build.build_admin_ui(opts.admin_ui)
    built = sum(SERVER_BUILDERS[name](opts, version) for name in dict.fromkeys(os_names))
    if built == 0:
        raise gb_build.BuildError(f"no server target matched (--arch {opts.arch})")


# ---------------------------------------------------------------------------------------------
# Upload


def upload_files() -> list[Path]:
    server_version = gb_build.read_version()
    return [*client_files(read_client_version()), *sorted((SERVER_DIR / "dist").glob(f"gligabao_server-{server_version}-*.whl"))]


def upload(test_pypi: bool,
           assume_yes: bool) -> None:
    files = upload_files()
    if not files:
        raise gb_build.BuildError(
            f"nothing to upload: no files for gligabao {read_client_version()} / gligabao-server {gb_build.read_version()} "
            "in the dist/ directories; build first")
    target = "TestPyPI" if test_pypi else "PyPI"
    gb_build.log(f"To upload to {target}:")
    for f in files:
        print(f"    {f.relative_to(ROOT).as_posix()}")
    if not assume_yes and input(f"Upload {len(files)} file(s) to {target}? [y/N] ").strip().lower() not in ("y", "yes"):
        raise gb_build.BuildError("aborted")
    env = {**os.environ, "TWINE_USERNAME": os.environ.get("TWINE_USERNAME", "__token__")}
    repo = ["--repository", "testpypi"] if test_pypi else []
    gb_build.run(python_module("twine", "upload", *repo, *(str(f) for f in files)), env=env)
    gb_build.log(f"Uploaded {len(files)} file(s) to {target}")


# ---------------------------------------------------------------------------------------------
# Command line


def make_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Build and upload the PyPI packages gligabao (client) and gligabao-server (one wheel per platform).",
        epilog="Auth: set TWINE_PASSWORD to your API token (pypi-...); TWINE_USERNAME defaults to __token__. "
               "Otherwise twine uses ~/.pypirc or prompts.")
    build = parser.add_argument_group("build flags")
    build.add_argument("--build_client", action="store_true", help="build the Python client (wheel + sdist)")
    build.add_argument("--build_server", action="store_true", help="build the server for all platforms (= the flags below)")
    build.add_argument("--build_server_linux", action="store_true", help="the server for Linux (amd64, arm64; wheels also tagged musllinux, the binary is static)")
    build.add_argument("--build_server_darwin", action="store_true", help="the server for macOS (amd64, arm64)")
    build.add_argument("--build_server_windows", action="store_true", help="the server for Windows (amd64, arm64)")
    build.add_argument("--build_server_host", action="store_true", help="the server for the platform this program runs on")
    build.add_argument("--arch", choices=gb_build.ARCHES, help="restrict the server builds above to one architecture")
    build.add_argument("--sargo", action="store_true", help="include sargo in the wheels of every requested platform (default: Linux only; macOS and Windows need a Mac / Windows machine)")
    build.add_argument("--no-sargo", action="store_true", help="build the wheels without sargo, also on Linux")
    build.add_argument("--no-admin-ui", action="store_true", help="build the server without the embedded admin UI (default: embedded; needs node + npm)")
    up = parser.add_argument_group("upload flags")
    up.add_argument("--upload", action="store_true", help="upload every built wheel/sdist of the current versions (build flags may precede it)")
    up.add_argument("--test", action="store_true", help="upload to TestPyPI instead of PyPI")
    up.add_argument("--yes", "-y", action="store_true", help="do not ask for confirmation")
    return parser


def requested_oses(args: argparse.Namespace) -> list[str]:
    return [name for name, wanted in (
        ("linux", args.build_server or args.build_server_linux),
        ("darwin", args.build_server or args.build_server_darwin),
        ("windows", args.build_server or args.build_server_windows),
        (gb_build.host_os(), args.build_server_host),
    ) if wanted]


def main(argv: Sequence[str] | None = None) -> int:
    parser = make_parser()
    args = parser.parse_args(argv)
    os_names = requested_oses(args)
    if not (args.build_client or os_names or args.upload):
        parser.error("nothing to do: give a build flag and/or --upload")
    if args.test and not args.upload:
        parser.error("--test only applies with --upload")
    if args.sargo and args.no_sargo:
        parser.error("--sargo and --no-sargo exclude each other")
    if args.arch and not os_names:
        parser.error("--arch only applies to the --build_server* flags")
    try:
        ensure_python_tools()
        if args.build_client:
            build_client()
        if os_names:
            build_server(os_names, Options(admin_ui=not args.no_admin_ui, arch=args.arch, sargo=True if args.sargo else False if args.no_sargo else None))
        if args.upload:
            upload(args.test, args.yes)
    except gb_build.BuildError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
