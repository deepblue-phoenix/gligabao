# Building and publishing the PyPI packages

gligabao is published on PyPI as two packages, so that people who only call gligabao do not download the server:

| Package | Directory | What it is | Wheels |
|---|---|---|---|
| `gligabao` | `clients/py` | the Python client | one pure-Python wheel and an sdist |
| `gligabao-server` | `clients/py_server` | the server: the Go binary (admin UI embedded) and the default `gligabao.toml` | one wheel per platform |

```sh
pip install gligabao            # the client
pip install gligabao-server     # the server: `gligabao-server serve` (or `gligabao serve`)
```

Both are built and uploaded by `ops/pypi.py`. Like [`ops/build.py`](build.md), it is portable Python (standard library only) and runs natively on Linux, macOS and Windows: `python ops/pypi.py ...` (Windows: `py ops/pypi.py ...`). `./run_pypi.sh` is a thin wrapper for Linux/macOS that calls it with the same flags.

## Where the code is

All of the packaging logic is in `ops/`; `clients/py_server` only holds the package sources.

| File | What it does |
|---|---|
| `ops/pypi.py` | The pipeline: tests, staging directories, wheels, `twine check`, upload. Uses `ops/build.py` for the admin UI, sargo and the Go server binary. |
| `ops/wheel_platform_tag_hook.py` | The hatchling build hook that marks the server wheel as platform specific and sets its tag from `GLIGABAO_WHEEL_TAG`. It is copied next to `pyproject.toml` in each staging directory, where hatchling finds it. |
| `ops/build.py` | The Go, npm and cargo build steps (`server` command, `build_sargo`, `build_admin_ui`). |
| `run_pypi.sh` | Wrapper: `python3 ops/pypi.py "$@"`. |

`ops/pypi.py` has one build function per platform for the server wheels: `build_server_linux`, `build_server_macos` and `build_server_windows`. Each holds what is specific to its platform (the targets with their wheel platform tags, the executable name, whether sargo is included by default), and all share `build_server_wheel`. The Go binary cross-compiles, so any machine can build the wheels of any platform; `--build_server_host` builds those of the platform the program runs on.

## How the server package works

pip cannot compile Go, so the wheels contain a binary that was cross-compiled beforehand (the same approach as ruff or uv). For each target `ops/pypi.py`:

1. builds `./cmd/gligabao` with `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch>` (the build is pure Go and static, so cross-compiling needs nothing but the Go toolchain); with [sargo](#sargo-in-the-wheels) it first builds sargo and puts it in `rust/sargo/bin/`, where the Go build embeds it;
2. assembles a staging directory `build/pypi/server-<os>-<arch>/` with the package sources, the binary and a copy of `build/gligabao.toml`, and writes the version of the `VERSION` file into `_version.py` there (the server package has the version of the server; the client has its own, in `clients/py/pyproject.toml`);
3. builds a wheel from it with hatchling. `ops/wheel_platform_tag_hook.py` marks the wheel as platform specific and sets its tag from `GLIGABAO_WHEEL_TAG`.

`pip install gligabao-server` then picks the wheel matching the machine. An sdist is not published (it would have no binary). Nothing is generated inside `clients/py_server` itself; its `_bin/` directory only exists in the staging directories.

| `--build_server_*` | Targets | Wheel platform tag |
|---|---|---|
| `linux` | amd64, arm64 | `manylinux2014_*.musllinux_1_1_*` (one static binary serves glibc and musl systems) |
| `darwin` | amd64, arm64 | `macosx_12_0_*` |
| `windows` | amd64, arm64 | `win_amd64`, `win_arm64` |

### sargo in the wheels

[sargo](../rust/sargo/README.md) (the Rust model router) is embedded into the server binary of a wheel (`gligabao sargo serve`, see [build.md](build.md)). Rust does not cross-compile like Go, so it can only be built on a machine of its own OS:

| Platform | sargo in the wheel |
|---|---|
| Linux | **Yes, by default**: built as a static musl binary and embedded, for the CPU architecture of the build machine. The other architecture cannot be built on it (no musl cross compiler): build with `--arch` of the machine, or `--no-sargo`. |
| macOS | No, only the Go server, unless `--sargo` is given (then run the build on a Mac). |
| Windows | No, only the Go server, unless `--sargo` is given (then run the build on Windows). |

`--sargo` asks for sargo in every requested platform, `--no-sargo` leaves it out everywhere. A request that this machine cannot fulfil fails before anything is built, saying which OS or architecture to build on. So on an amd64 Linux machine: `python ops/pypi.py --build_server_linux --arch amd64`.

### What is installed

- `gligabao_server/_bin/gligabao` (`gligabao.exe` on Windows), executable.
- `gligabao_server/_bin/gligabao.toml`, the repo's `build/gligabao.toml`. The binary reads its config from next to itself unless `--config` is given, so the packaged file is the default. It lives in site-packages and is replaced when the package is upgraded: keep your own settings in a file elsewhere and run `gligabao-server serve --config <path>`.
- The commands `gligabao-server` and `gligabao`, which run the binary with the same arguments; `gligabao_server.binary_path()` and `gligabao_server.config_path()` for Python code.
- Not included: the Python sidecar (`sidecar/`, training). Use the Docker image for it. The directories the config names (`datasets_training`, `evals_*`, `ontology`) are not shipped either; they are resolved like for any other binary, relative to the database or as given by the flags.
- The database and its neighbours (`--db`, default `data/gligabao.db`) are created relative to the current directory, not in site-packages.

## Building

Building and uploading are separate steps: building writes into the `dist/` directory of each package, uploading sends what is in those directories. Giving both in one call does both, building first.

```sh
./run_pypi.sh --build_client                       # clients/py/dist: wheel + sdist
./run_pypi.sh --build_server                       # clients/py_server/dist: all platforms (needs --arch/--no-sargo on Linux if it cannot build sargo for both)
./run_pypi.sh --build_server_linux                 # one OS (both architectures)
./run_pypi.sh --build_server_linux --arch amd64    # one platform (with sargo)
./run_pypi.sh --build_server_darwin --build_server_windows --arch arm64   # Go server only
./run_pypi.sh --build_server_host                  # the platform this machine runs
./run_pypi.sh --build_server --no-admin-ui         # without the embedded admin UI (no node needed)
./run_pypi.sh --build_server_linux --no-sargo      # Linux without sargo
```

- The admin UI is built once per call (`admin_ui/`, needs node and npm, like `ops/build.py`) and embedded in every binary. `--no-admin-ui` leaves it out.
- Python tools (`build`, `twine`, `pytest`) are installed with pip if missing.
- Tests run first: the client tests before the client is built, the launcher tests before the server wheels.
- A build replaces only the file of its own target and version. Building Linux today and macOS tomorrow leaves both in `dist/`, and `dist/` may keep wheels of older versions (they are never uploaded).
- Each wheel is checked with `twine check`.

## Uploading

```sh
export TWINE_PASSWORD=pypi-...                     # an API token; TWINE_USERNAME defaults to __token__
./run_pypi.sh --upload --test                      # to TestPyPI
./run_pypi.sh --upload                             # to PyPI
./run_pypi.sh --build_client --build_server --upload
```

`--upload` sends every file of the current versions that is in the `dist/` directories: the client's wheel and sdist (version from `clients/py/pyproject.toml`) and all server wheels that were built (version from `VERSION`). It lists them and asks for confirmation (`--yes` skips it). If a platform was never built, it is not uploaded, so build all platforms before the release. PyPI never accepts the same file twice: to republish, bump the version.

Release steps for a new server version: bump `VERSION` and the changelog (see [AGENTS.md](../AGENTS.md)); build everything; upload to TestPyPI and try `pip install -i https://test.pypi.org/simple/ gligabao-server`; upload to PyPI. A new client version only needs `version` in `clients/py/pyproject.toml` raised.

## Notes

- Wheel size is about 20 MB per platform without the admin UI, a bit more with it (PyPI's limit is 100 MB per file).
- The minimum macOS version of the wheels follows the Go toolchain's; raise `macosx_12_0_*` in `ops/pypi.py` (`MACOS_TARGETS`) when Go does.
- Building Windows or macOS wheels on Linux works, but only the host's platform can be run; try the others on a real machine before a release.
