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

Both are built and uploaded by `./run_pypi.sh`.

## How the server package works

pip cannot compile Go, so the wheels contain a binary that was cross-compiled beforehand (the same approach as ruff or uv). For each target `run_pypi.sh`:

1. builds `./cmd/gligabao` with `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch>` (the build is pure Go and static, so cross-compiling needs nothing but the Go toolchain);
2. assembles a staging directory `build/pypi/server-<os>-<arch>/` with the package sources, the binary and a copy of `build/gligabao.toml`, and writes the version of the `VERSION` file into `_version.py` there (the server package has the version of the server; the client has its own, in `clients/py/pyproject.toml`);
3. builds a wheel from it with hatchling. `hatch_build.py` marks the wheel as platform specific and sets its tag from `GLIGABAO_WHEEL_TAG`.

`pip install gligabao-server` then picks the wheel matching the machine. An sdist is not published (it would have no binary). Nothing is generated inside `clients/py_server` itself; its `_bin/` directory only exists in the staging directories.

| `--build_server_*` | Targets | Wheel platform tag |
|---|---|---|
| `linux` | amd64, arm64 | `manylinux2014_*.musllinux_1_1_*` (one static binary serves glibc and musl systems) |
| `darwin` | amd64, arm64 | `macosx_12_0_*` |
| `windows` | amd64, arm64 | `win_amd64`, `win_arm64` |

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
./run_pypi.sh --build_server                       # clients/py_server/dist: all platforms
./run_pypi.sh --build_server_linux                 # one OS (both architectures)
./run_pypi.sh --build_server_linux --arch amd64    # one platform
./run_pypi.sh --build_server_darwin --build_server_windows --arch arm64
./run_pypi.sh --build_server --no-admin-ui         # without the embedded admin UI (no node needed)
```

- The admin UI is built once per call (`admin_ui/`, needs node and npm, like `./run_build.sh`) and embedded in every binary. `--no-admin-ui` leaves it out.
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
- The minimum macOS version of the wheels follows the Go toolchain's; raise `macosx_12_0_*` in `run_pypi.sh` when Go does.
- Building Windows or macOS wheels on Linux works, but only the host's platform is run by the script; try the others on a real machine before a release.
