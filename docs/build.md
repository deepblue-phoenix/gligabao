# Building gligabao and sargo (`ops/build.py`)

All build logic lives in one Python program, `ops/build.py` (standard library only, Python 3.9+). It builds, for the target OS and CPU:

1. the **admin UI** (`admin_ui/`, npm; embedded into the server binary),
2. **sargo**, the Rust model router (`rust/sargo/`, cargo), optionally embedded into the server binary,
3. the **Go binaries** in `build/`: `gligabao` (the server and CLI), `gligabao_agent`, `gligabao_worker`, and always `gligabao_agent_linux_amd64` (baked into the Modal app by the ML sidecar, see [ml_sidecar.md](training/ml_sidecar.md#the-research-agent-in-modal)).

**It must run natively on every platform where building is done.** A Windows user builds the Windows package on Windows, a Mac user the macOS one, and so on, without bash. The shell scripts of the repo root (`run_build.sh`, `run_build_rust.sh`, part of `run_pypi.sh`) are only wrappers for Linux/macOS that call it. See "Build pipeline" in `AGENTS.md`.

## Usage

```sh
python3 ops/build.py all                     # full pipeline for this machine (Windows: `py ops/build.py all`)
python3 ops/build.py all --sargo             # same, with sargo built statically and embedded
python3 ops/build.py all --os linux --arch arm64 --no-admin-ui   # Go cross-compile
python3 ops/build.py admin-ui                # only admin_ui/dist
python3 ops/build.py sargo [--static] [--embed]   # only sargo
python3 ops/build.py go --os windows         # only the Go binaries
python3 ops/build.py server --out FILE       # one stripped release server binary (used by `ops/pypi.py`)
```

| Option | Meaning |
|---|---|
| `--os linux\|darwin\|windows`, `--arch amd64\|arm64` | The target. Default: the environment variables `GOOS` / `GOARCH`, then this machine. |
| `--sargo` (`all`) or `BUILD_SARGO=true` | Build sargo as a static binary and embed it into `build/gligabao`, which then runs it with `gligabao sargo serve`. Without it, a stale `rust/sargo/bin/sargo` is deleted so it is not embedded. |
| `--no-admin-ui` or `BUILD_ADMIN_UI=false` | Build without the admin UI (stale `admin_ui/dist` files are deleted, `.gitkeep` stays). |
| `--static` / `--embed` (`sargo`) | `--static` builds for the target triple, statically linked; `--embed` implies it and copies the result to `rust/sargo/bin/sargo`. Without either, a plain dynamic `cargo build --release` for this machine (`rust/sargo/target/release/sargo`, what `run_server_sargo.sh` runs). |

Shell wrappers: `./run_build.sh [--no-admin-ui] [--sargo]` is `ops/build.py all`, `./run_build_rust.sh [--static] [--embed]` is `ops/build.py sargo`. `PYTHON=...` picks the interpreter.

## How it is organised

- **Pure helpers** compute things from data: `Target` (OS + arch, with the `.exe` suffix and the Rust target triple), `target_from`, `go_env`, `go_release_args`, `sargo_env`.
- **One pipeline per platform**: `build_linux`, `build_macos`, `build_windows` (table `PIPELINES`). Each runs the admin UI step, the sargo step (if asked), then the Go binaries, for its OS.
- **One sargo builder per platform**: `build_sargo_linux`, `build_sargo_macos`, `build_sargo_windows` (table `SARGO_BUILDERS`).
- **What cross-compiles:** Go always does (`CGO_ENABLED=0`, so no C toolchain), from any host to any of the 3 OSes. Rust does not without a cross linker, so sargo is built only on its own OS. Asking for `--sargo` for another OS fails at the start, before any slow step, saying which OS to run the build on.
- **Embedding:** `rust/sargo/embed.go` embeds `rust/sargo/bin/sargo` (the file has this name on every OS) and `rust/sargo/config.toml`. `bin/` holds only `.gitkeep` on a fresh checkout, so the Go build always works; the gligabao binary then reports sargo as not embedded.
- **Output:** `build/gligabao[.exe]`, `build/gligabao_agent[.exe]`, `build/gligabao_worker[.exe]`, `build/gligabao_agent_linux_amd64`. The server is built plain (its version comes from the embedded `VERSION`); the agent and worker are `-trimpath -s -w` with `-X main.version=<VERSION>`.

## Linux

- **Needs:** Go, Node + npm (unless `--no-admin-ui`), and for `--sargo` cargo (rustup, plus `build-essential`) and **`musl-tools`** (`sudo apt install -y musl-tools` on Ubuntu).
- **sargo:** a fully static binary for the target `<x86_64|aarch64>-unknown-linux-musl`, so it runs on any Linux (glibc or musl) of the same CPU architecture. `rustup target add` is run for you. The TLS dependency (`ring`) compiles C code; cc-rs looks for `<arch>-linux-musl-gcc`, but Ubuntu's `musl-tools` only ships `musl-gcc`, so the build sets `CC_<triple>=musl-gcc`.
- **Architecture:** sargo is built for the host architecture only (a cross musl compiler would be needed); `--arch` different from the host fails with a message. Build on an arm64 machine for arm64 sargo. The Go binaries cross-compile freely (`GOOS=linux GOARCH=arm64 python3 ops/build.py all --no-admin-ui`).
- **Result:** `build/gligabao` is static (CGO off) and embeds the static sargo.

## macOS

- **Needs:** Go, Node + npm, and for `--sargo` cargo (rustup) and the Xcode command line tools (`xcode-select --install`, the linker).
- **sargo:** target `aarch64-apple-darwin` (Apple silicon, `arm64`) or `x86_64-apple-darwin` (Intel, `amd64`); `rustup target add` is run for you. Both architectures build on either kind of Mac, so one machine can produce both. macOS binaries cannot be fully static, but sargo links only system libraries, so `--static` just means "build for the explicit target triple".
- **Embedding:** the embedded sargo must match the gligabao binary: build both for the same `--arch` (`python3 ops/build.py all --os darwin --arch amd64 --sargo` on an Apple silicon Mac).
- **Result:** `build/gligabao` (a Mach-O binary, CGO off). The Go binaries are not code-signed or notarized by this program; do that separately for distribution.

## Windows

- **Needs:** Go, Node + npm (`npm.cmd` is found automatically), and for `--sargo` cargo (download `rustup-init.exe` from <https://rustup.rs>) with the **Visual Studio C++ Build Tools** (the MSVC linker). Run it from a normal terminal: `py ops/build.py all --sargo` (or `python`). No bash, `make` or MSYS is needed.
- **sargo:** target `x86_64-pc-windows-msvc` or `aarch64-pc-windows-msvc`, built with `RUSTFLAGS=-C target-feature=+crt-static` so the `.exe` needs no Visual C++ redistributable. For `arm64` install the ARM64 build tools component of Visual Studio.
- **Executables:** every output gets `.exe` (`gligabao.exe`, `gligabao_agent.exe`, `gligabao_worker.exe`; `gligabao_agent_linux_amd64` is a Linux file and has no suffix). The embedded sargo is stored as `bin/sargo` but extracted as `sargo.exe` at run time (`internal/sargo/server.go`), because Windows only runs `.exe` files.
- **Paths:** the program only uses `pathlib` and argument lists (never a shell), so paths with spaces and backslashes are safe.

## Not done by this program

- The container image (`Dockerfile`, `run_build_container.sh`; it contains the ML sidecar too) builds in their own Docker stages. The `Dockerfile` does not yet include sargo (see `leftover_tasks/`).
- The PyPI packaging (staging, wheels, twine, upload) is `ops/pypi.py`, which uses `ops/build.py` for the admin UI, sargo and the server binary. See [pypi.md](pypi.md).
- Running one OS's pipeline for another OS's sargo (e.g. in CI, use one runner per OS and run `all --sargo` on each).
