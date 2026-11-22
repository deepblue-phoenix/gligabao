# Embed sargo in the Docker image build and use the embedded server from the app

**Date**: 2026-11-22
**Version**: 0.230.0
**Status**: open

## Context

0.230.0 added the packaging of sargo into the gligabao binary (`rust/sargo/embed.go`, `./run_build.sh --sargo`), the Go server mode
(`sargo.StartServer`, `internal/sargo/server.go`) and `gligabao sargo serve`. It is opt-in and nothing in gligabao itself starts the
server yet: the scope was the packaging and the server mode only. `Dockerfile` and `run_build_container.sh` still build gligabao without
sargo, and the LLM call sites still do not use sargo (see the roadmap item about the inference wrapper).

## Remaining work

- [ ] `Dockerfile`: add a Rust build stage that produces the static sargo (`./run_build_rust.sh --embed` logic, musl target) and copy it to `rust/sargo/bin/sargo` before the Go build, so the image's `gligabao` embeds sargo.
- [ ] Wire the inference wrapper of the roadmap to the started sargo (`serve` starts it since 0.231.0, `sargo = true`, but nothing calls it yet): use its address (`server.address` of the sargo config) with `sargo.New`.
- [ ] Cross-architecture builds: `run_build_rust.sh --embed` embeds the host architecture only; for `GOARCH` != host, build sargo for the matching musl target.

## Progress

- 2026-11-22 (0.231.0): `gligabao serve` starts the embedded sargo (`sargo`, `sargo_config`, `--sargo`).

## How to pick it up

Read `rust/sargo/README.md` ("Embedded in the gligabao binary"), `internal/sargo/server.go` and `docs/roadmap/ROADMAP.md`. Check with
`./run_build.sh --sargo && ./build/gligabao sargo serve` and `./run_tests.sh -g`. Done when the container image answers `gligabao sargo serve` and the roadmap item is wired.
