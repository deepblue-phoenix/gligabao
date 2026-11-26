#!/usr/bin/env python3
"""Build tool of gligabao: every build sequence and build tool invocation (npm, cargo, go) lives here.

The shell scripts of the repo root (run_build.sh, run_build_rust.sh) are thin wrappers around this file (run_pypi.sh wraps ops/pypi.py, which uses it).
Only the Python standard library is used (Python 3.9+), so it runs the same on Linux, macOS and Windows.

REQUIREMENT: this program must run on every platform where building is done. A user on Windows runs it on Windows and gets the full
gligabao package for Windows; the same for macOS and Linux (`python ops/build.py all --sargo`, or `py ops/build.py ...` on Windows).
So it uses only portable Python (no shell, no sh/find/cp/rm), and the shell scripts of the repo root are optional wrappers only.

There is one pipeline per platform (build_linux, build_macos, build_windows). Each one builds, for the target OS and CPU:

  1. the admin UI (npm, embedded into the server binary),
  2. sargo, the Rust model router (cargo), optionally embedded into the server binary,
  3. the parsers of esbuild and typescript-go (generate_esbuild: `go run` of the two gen_* tools in internal/memory/memory_code, needed by every Go build below),
  4. the Go binaries: build/gligabao (server), build/gligabao_agent, build/gligabao_worker, and always
     build/gligabao_agent_linux_amd64 (baked into the Modal app by the ML sidecar).

Go cross-compiles from any host (CGO is off), so the Go binaries can be built for any target OS/arch from any machine.
Rust cannot do that without a cross linker, so sargo is built only for the host OS (macOS: both CPU architectures;
Linux: the host architecture, static musl; Windows: MSVC target, statically linked C runtime).

Usage (run `python3 ops/build.py --help`, or `<command> --help`):

  python3 ops/build.py all     [--os linux|darwin|windows] [--arch amd64|arm64] [--sargo] [--no-admin-ui]
  python3 ops/build.py admin-ui [--no-admin-ui]
  python3 ops/build.py sargo   [--os ..] [--arch ..] [--static] [--embed]
  python3 ops/build.py go      [--os ..] [--arch ..]
  python3 ops/build.py esbuild                                         (only the generate step; for builds that are not made here)
  python3 ops/build.py server  --out FILE [--os ..] [--arch ..]       (release server binary, used by ops/pypi.py)

--os and --arch default to the environment variables GOOS and GOARCH, then to the host.
"""

from __future__ import annotations

import argparse
import os
import platform
import shutil
import subprocess
import sys
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Callable, Mapping, Sequence

ROOT = Path(__file__).resolve().parent.parent
ADMIN_UI = ROOT / "admin_ui"
SARGO_DIR = ROOT / "rust" / "sargo"
SARGO_BIN_DIR = SARGO_DIR / "bin"  # embedded by rust/sargo/embed.go
BUILD_DIR = ROOT / "build"

OSES = ("linux", "darwin", "windows")
ARCHES = ("amd64", "arm64")

# The Go build tag that compiles the TypeScript/JavaScript code extraction (internal/memory/memory_code/typescript.go). It imports
# internal/memory/memory_code/esparser and tsparser, copies of the parsers of esbuild and typescript-go that are NOT in git (about 6 MB of third party code that would sit
# in the history for good): they are made by gen_esbuild and gen_tsgo, so generate_esbuild() must run before any Go build that passes the tag. Without the
# tag the code compiles with no generate step (a stub reports TypeScript as unsupported), which is what plain `go build` / `go get` give.
# Every Go build made by this file passes the tag, so the binaries it makes (and ops/pypi.py's) always support TypeScript. A build of
# gligabao_int (the sibling repo that embeds gligabao) must do the same: its run_build.sh calls `python3 ops/build.py esbuild` of this repo and
# builds with `-tags tsjs_css`.
ESBUILD_TAG = "tsjs_css"
ESBUILD_GEN_PACKAGES = (
    "./internal/memory/memory_code/gen_esbuild",  # esparser/: the parser of esbuild (TypeScript/JavaScript code, CSS)
    "./internal/memory/memory_code/gen_tsgo",  # tsparser/: the parser of typescript-go, the official TypeScript one (TypeScript types)
)


# ---------------------------------------------------------------------------------------------
# Data


@dataclass(frozen=True)
class Target:
    """An operating system (Go names: linux, darwin, windows) and CPU architecture (Go names: amd64, arm64)."""

    os: str
    arch: str

    @property
    def exe(self) -> str:
        return ".exe" if self.os == "windows" else ""

    @property
    def rust_arch(self) -> str:
        return {"amd64": "x86_64", "arm64": "aarch64"}[self.arch]

    @property
    def rust_triple(self) -> str:
        return {
            "linux": f"{self.rust_arch}-unknown-linux-musl",
            "darwin": f"{self.rust_arch}-apple-darwin",
            "windows": f"{self.rust_arch}-pc-windows-msvc",
        }[self.os]


@dataclass(frozen=True)
class Options:
    target: Target
    admin_ui: bool = True
    sargo: bool = False


class BuildError(Exception):
    """A failure to report to the user without a stack trace."""


# ---------------------------------------------------------------------------------------------
# Pure helpers


def host_os() -> str:
    return {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system(), "")


def host_arch() -> str:
    return {
        "x86_64": "amd64",
        "amd64": "amd64",
        "aarch64": "arm64",
        "arm64": "arm64",
    }.get(platform.machine().lower(), "")


def host_target() -> Target:
    return Target(os=host_os(), arch=host_arch())


def target_from(os_name: str | None,
                arch: str | None,
                env: Mapping[str, str]) -> Target:
    """Explicit value, else GOOS / GOARCH of the environment, else the host."""
    host = host_target()
    target = Target(
        os=os_name or env.get("GOOS") or host.os,
        arch=arch or env.get("GOARCH") or host.arch,
    )
    if target.os not in OSES:
        raise BuildError(f"unsupported OS {target.os!r}; use one of {', '.join(OSES)}")
    if target.arch not in ARCHES:
        raise BuildError(f"unsupported architecture {target.arch!r}; use one of {', '.join(ARCHES)}")
    return target


def read_version() -> str:
    return "".join((ROOT / "VERSION").read_text().split())


def go_env(target: Target,
           base: Mapping[str, str]) -> dict[str, str]:
    return {**base, "CGO_ENABLED": "0", "GOOS": target.os, "GOARCH": target.arch}


def generate_esbuild() -> None:
    """Makes internal/memory/memory_code/esparser and tsparser (the copies of the parsers of esbuild and typescript-go; see ESBUILD_TAG). Quick when they are up to date.

    It runs `go` from ROOT: the copy always lands in this repo, also when the build that needs it is the one of gligabao_int.
    """
    require_tool("go", "Install Go (https://go.dev/dl).")
    for package in ESBUILD_GEN_PACKAGES:
        run(["go", "run", package], cwd=ROOT)


def go_release_args(version: str,
                    out: Path,
                    package: str,
                    stamp_version: bool) -> list[str]:
    """Stripped, reproducible-path build. The main package of the agent and worker take the version through -X."""
    ldflags = "-s -w" + (f" -X main.version={version}" if stamp_version else "")
    return ["go", "build", "-tags", ESBUILD_TAG, "-trimpath", "-ldflags", ldflags, "-o", str(out), package]


def sargo_env(target: Target,
              base: Mapping[str, str],
              musl_gcc: str | None) -> dict[str, str]:
    """Environment of the static cargo build of the target."""
    if target.os == "linux":
        # The TLS dependency (ring) compiles C; cc-rs looks for <arch>-linux-musl-gcc, but Ubuntu's musl-tools only has musl-gcc.
        return {**base, f"CC_{target.rust_triple.replace('-', '_')}": musl_gcc or "musl-gcc"}
    if target.os == "windows":
        # Link the C runtime statically so the .exe needs no Visual C++ redistributable.
        flags = (base.get("RUSTFLAGS", "") + " -C target-feature=+crt-static").strip()
        return {**base, "RUSTFLAGS": flags}
    return dict(base)


# ---------------------------------------------------------------------------------------------
# Side effects: running tools


def log(message: str) -> None:
    print(f"==> {message}", flush=True)


def run(cmd: Sequence[str],
        cwd: Path = ROOT,
        env: Mapping[str, str] | None = None) -> None:
    print("+ " + " ".join(cmd), flush=True)
    search_path = env.get("PATH") if env is not None else None
    resolved = [shutil.which(cmd[0], path=search_path) or cmd[0], *cmd[1:]]  # finds npm.cmd and cargo.exe on Windows
    try:
        subprocess.run(resolved, cwd=cwd, env=dict(env) if env is not None else None, check=True)
    except FileNotFoundError as err:
        raise BuildError(f"{cmd[0]} not found: {err}") from err
    except subprocess.CalledProcessError as err:
        raise BuildError(f"{' '.join(cmd)} failed (exit {err.returncode})") from err


def require_tool(name: str,
                 hint: str) -> None:
    if shutil.which(name) is None:
        raise BuildError(f"{name} is not installed. {hint}")


def clear_dir(path: Path) -> None:
    """Delete everything in path except the .gitkeep placeholder."""
    if path.is_dir():
        for entry in path.iterdir():
            if entry.name != ".gitkeep":
                shutil.rmtree(entry) if entry.is_dir() else entry.unlink()


# ---------------------------------------------------------------------------------------------
# Steps (shared by the pipelines)


def build_admin_ui(enabled: bool) -> None:
    """Builds admin_ui/dist, which the Go build embeds; when disabled, drops stale artifacts so they are not embedded."""
    if not enabled:
        clear_dir(ADMIN_UI / "dist")
        return
    require_tool("npm", "Install Node.js (https://nodejs.org) or pass --no-admin-ui.")
    if not (ADMIN_UI / "node_modules").is_dir():
        run(["npm", "ci"], cwd=ADMIN_UI)
    run(["npm", "run", "build"], cwd=ADMIN_UI)


def cargo_path_env() -> dict[str, str]:
    """rustup installs cargo in ~/.cargo/bin; a fresh shell may not have it on PATH yet."""
    cargo_bin = Path.home() / ".cargo" / "bin"
    path = os.environ.get("PATH", "")
    return {**os.environ, "PATH": f"{cargo_bin}{os.pathsep}{path}"} if cargo_bin.is_dir() else dict(os.environ)


def ensure_cargo(env: Mapping[str, str]) -> None:
    if shutil.which("cargo", path=env.get("PATH")) is None:
        raise BuildError(
            "cargo (Rust) is not installed. Install it with rustup (https://rustup.rs):\n"
            "  Linux/macOS: curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh\n"
            "  Windows:     download rustup-init.exe from https://rustup.rs (needs the Visual Studio C++ Build Tools)\n"
            "  Linux also needs a C compiler: sudo apt install -y build-essential"
        )


def embed_sargo(binary: Path) -> None:
    SARGO_BIN_DIR.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(binary, SARGO_BIN_DIR / "sargo")  # the name embed.go expects, on every OS
    print("copied to rust/sargo/bin/sargo (embedded by the next go build)")


def drop_embedded_sargo() -> None:
    """Drop a stale binary so it is not embedded."""
    clear_dir(SARGO_BIN_DIR)


def go_server_binary(target: Target,
                     out: Path,
                     release: bool,
                     version: str) -> None:
    """The server (cmd/gligabao). Its version comes from the embedded VERSION file, so no -X is needed."""
    generate_esbuild()
    out.parent.mkdir(parents=True, exist_ok=True)
    cmd = (
        go_release_args(version, out, "./cmd/gligabao", stamp_version=False)
        if release
        else ["go", "build", "-tags", ESBUILD_TAG, "-o", str(out), "./cmd/gligabao"]
    )
    run(cmd, env=go_env(target, os.environ))


def go_binaries(target: Target,
                admin_ui_enabled: bool | None = None,
                sargo_enabled: bool | None = None) -> None:
    """build/gligabao (+ agent, worker, and the Linux amd64 agent for Modal), cross-compiled for target."""
    require_tool("go", "Install Go (https://go.dev/dl).")
    version = read_version()
    BUILD_DIR.mkdir(exist_ok=True)
    generate_esbuild()  # before the first Go build with the tag (the agent and the worker are built with it too)

    go_server_binary(target, BUILD_DIR / f"gligabao{target.exe}", release=False, version=version)
    details = "" if admin_ui_enabled is None else (
        f" (admin UI embedded: {str(admin_ui_enabled).lower()}, sargo embedded: {str(sargo_enabled).lower()})")
    print(f"Built ./build/gligabao{target.exe}{details}")

    # The agent and the worker: pure Go, stripped, with the version from VERSION (they cannot embed it, VERSION is outside their package).
    for name in ("gligabao_agent", "gligabao_worker"):
        out = BUILD_DIR / f"{name}{target.exe}"
        run(go_release_args(version, out, f"./cmd/{name}", stamp_version=True), env=go_env(target, os.environ))
        print(f"Built ./build/{out.name}")

    # The same agent for the Modal containers (Linux, x86-64), whatever this machine is.
    modal = Target(os="linux", arch="amd64")
    out = BUILD_DIR / "gligabao_agent_linux_amd64"
    run(go_release_args(version, out, "./cmd/gligabao_agent", stamp_version=True), env=go_env(modal, os.environ))
    print(f"Built ./build/{out.name}")


# ---------------------------------------------------------------------------------------------
# sargo (Rust), one function per platform


def cargo_build(target: Target,
                static: bool,
                env: Mapping[str, str]) -> Path:
    """Runs cargo for sargo; returns the path of the binary."""
    ensure_cargo(env)
    if not static:
        run(["cargo", "build", "--release"], cwd=SARGO_DIR, env=env)
        return SARGO_DIR / "target" / "release" / f"sargo{target.exe}"
    if shutil.which("rustup", path=env.get("PATH")):
        run(["rustup", "target", "add", target.rust_triple], cwd=SARGO_DIR, env=env)
    run(["cargo", "build", "--release", "--target", target.rust_triple], cwd=SARGO_DIR, env=env)
    return SARGO_DIR / "target" / target.rust_triple / "release" / f"sargo{target.exe}"


def build_sargo_linux(target: Target,
                      static: bool) -> Path:
    """Static musl binary (runs on any Linux of the same CPU architecture). Needs musl-tools (musl-gcc)."""
    if target.arch != host_arch():
        raise BuildError(
            f"sargo for linux/{target.arch} cannot be built on a {host_arch()} host (it needs a musl cross compiler); "
            "build it on a machine of that architecture"
        )
    env = cargo_path_env()
    if static and shutil.which("musl-gcc") is None:
        raise BuildError("musl-gcc not found (needed to compile the C code of dependencies for the musl target). "
                         "On Ubuntu: sudo apt install -y musl-tools")
    return cargo_build(target, static, sargo_env(target, env, "musl-gcc") if static else env)


def build_sargo_macos(target: Target,
                      static: bool) -> Path:
    """macOS binaries are never fully static, but link only system libraries. Both architectures build on either Mac."""
    env = cargo_path_env()
    return cargo_build(target, static, sargo_env(target, env, None) if static else env)


def build_sargo_windows(target: Target,
                        static: bool) -> Path:
    """MSVC target with the C runtime linked statically. Needs the Visual Studio C++ Build Tools."""
    env = cargo_path_env()
    return cargo_build(target, static, sargo_env(target, env, None) if static else env)


SARGO_BUILDERS: dict[str, Callable[[Target, bool], Path]] = {
    "linux": build_sargo_linux,
    "darwin": build_sargo_macos,
    "windows": build_sargo_windows,
}


def build_sargo(target: Target,
                static: bool,
                embed: bool) -> Path:
    check_sargo_host(target)
    # --embed implies static: the embedded binary must run anywhere the gligabao binary does.
    binary = SARGO_BUILDERS[target.os](target, static or embed)
    print(f"sargo binary: {binary.relative_to(ROOT)}")
    if embed:
        embed_sargo(binary)
    return binary


# ---------------------------------------------------------------------------------------------
# Pipelines, one per platform


def sargo_unsupported_reason(target: Target) -> str | None:
    """Why sargo cannot be built for target on this machine, or None when it can (Rust does not cross-compile like Go)."""
    if target.os != host_os():
        return (f"sargo for {target.os} cannot be built on a {host_os()} host: run this build on {target.os} "
                "(the Go binaries cross-compile, Rust does not)")
    if target.os == "linux" and target.arch != host_arch():
        return (f"sargo for linux/{target.arch} cannot be built on a {host_arch()} host (it needs a musl cross compiler); "
                "build it on a machine of that architecture")
    return None


def check_sargo_host(target: Target) -> None:
    reason = sargo_unsupported_reason(target)
    if reason is not None:
        raise BuildError(reason)


def pipeline(opts: Options) -> None:
    if opts.sargo:
        check_sargo_host(opts.target)  # fail before the slow steps
    build_admin_ui(opts.admin_ui)
    if opts.sargo:
        build_sargo(opts.target, static=True, embed=True)
    else:
        drop_embedded_sargo()
    go_binaries(opts.target, opts.admin_ui, opts.sargo)


def build_linux(opts: Options) -> None:
    """Linux: Go binaries (static, CGO off) and, with sargo, a static musl sargo for the host architecture."""
    pipeline(replace(opts, target=replace(opts.target, os="linux")))


def build_macos(opts: Options) -> None:
    """macOS: Go binaries for darwin and, with sargo, sargo for darwin (amd64 and arm64 both build on either Mac)."""
    pipeline(replace(opts, target=replace(opts.target, os="darwin")))


def build_windows(opts: Options) -> None:
    """Windows: .exe Go binaries and, with sargo, sargo.exe (MSVC, static C runtime)."""
    pipeline(replace(opts, target=replace(opts.target, os="windows")))


PIPELINES: dict[str, Callable[[Options], None]] = {
    "linux": build_linux,
    "darwin": build_macos,
    "windows": build_windows,
}


# ---------------------------------------------------------------------------------------------
# Command line


def add_target_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--os", choices=OSES, help="target OS (default: $GOOS, else this machine)")
    parser.add_argument("--arch", choices=ARCHES, help="target CPU architecture (default: $GOARCH, else this machine)")


def add_ui_arg(parser: argparse.ArgumentParser) -> None:
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--admin-ui", dest="admin_ui", action="store_true", default=None, help="build and embed the admin UI (default)")
    group.add_argument("--no-admin-ui", dest="admin_ui", action="store_false", help="build without the admin UI")


def admin_ui_enabled(args: argparse.Namespace) -> bool:
    if args.admin_ui is not None:
        return args.admin_ui
    return os.environ.get("BUILD_ADMIN_UI", "true").lower() != "false"


def make_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Build gligabao (Go) and sargo (Rust) for Linux, macOS and Windows.")
    commands = parser.add_subparsers(dest="command", required=True)

    all_cmd = commands.add_parser("all", help="the full pipeline of the target platform")
    add_target_args(all_cmd)
    add_ui_arg(all_cmd)
    all_cmd.add_argument("--sargo", action="store_true", default=None, help="build sargo statically and embed it (or BUILD_SARGO=true)")

    ui_cmd = commands.add_parser("admin-ui", help="build admin_ui/dist")
    add_ui_arg(ui_cmd)

    sargo_cmd = commands.add_parser("sargo", help="build sargo (Rust)")
    add_target_args(sargo_cmd)
    sargo_cmd.add_argument("--static", action="store_true", help="statically linked build for --os/--arch (Linux: musl)")
    sargo_cmd.add_argument("--embed", action="store_true", help="implies --static, then copy to rust/sargo/bin/sargo for the Go build")

    commands.add_parser("esbuild", help="generate the parsers of esbuild and typescript-go that the build tag 'tsjs_css' needs (no build)")

    go_cmd = commands.add_parser("go", help="build the Go binaries into build/")
    add_target_args(go_cmd)

    server_cmd = commands.add_parser("server", help="build one release server binary (stripped)")
    add_target_args(server_cmd)
    server_cmd.add_argument("--out", required=True, help="output file")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = make_parser().parse_args(argv)
    try:
        if args.command == "admin-ui":
            build_admin_ui(admin_ui_enabled(args))
        elif args.command == "sargo":
            if args.os is None and args.arch is None and not (args.static or args.embed) and "GOOS" not in os.environ:
                # Plain dev build: dynamic, for this machine, in target/release/.
                build_sargo(host_target(), static=False, embed=False)
            else:
                build_sargo(target_from(args.os, args.arch, os.environ), static=args.static, embed=args.embed)
        elif args.command == "esbuild":
            generate_esbuild()
        elif args.command == "go":
            go_binaries(target_from(args.os, args.arch, os.environ), None, None)
        elif args.command == "server":
            require_tool("go", "Install Go (https://go.dev/dl).")
            go_server_binary(target_from(args.os, args.arch, os.environ), Path(args.out).resolve(), release=True, version=read_version())
        else:
            target = target_from(args.os, args.arch, os.environ)
            sargo = args.sargo if args.sargo is not None else os.environ.get("BUILD_SARGO", "false").lower() == "true"
            PIPELINES[target.os](Options(target=target, admin_ui=admin_ui_enabled(args), sargo=sargo))
    except BuildError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
