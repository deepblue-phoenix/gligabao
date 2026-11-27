# syntax=docker/dockerfile:1

# The builder image: every tool the gligabao build pipeline (ops/build.py) needs (Go, Node/npm, Rust with the musl target, musl-gcc, Python 3),
# and the binaries built with it: `python3 ops/build.py all --sargo` runs while the image is built, so /src/build holds the result
# (gligabao with the admin UI and sargo embedded, gligabao_agent, gligabao_worker, gligabao_agent_linux_amd64).
#
#   docker build -f Dockerfile.builder -t gligabao-builder .
#   docker run --rm -v "$PWD/out:/out" gligabao-builder        # copies the binaries to ./out
#
# To rebuild after changing the source without rebuilding the image, mount the source:
#   docker run --rm -v "$PWD:/src" -v "$PWD/out:/out" gligabao-builder build
#
# The binaries are for Linux of the architecture of the image (static: Go with CGO off, sargo on musl).
FROM golang:1.26-bookworm

ARG NODE_MAJOR=22

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg python3 musl-tools build-essential pkg-config git \
    && mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" > /etc/apt/sources.list.d/nodesource.list \
    && apt-get update && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Rust (rustup) with the static musl target of this architecture, which is what ops/build.py builds sargo for on Linux.
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo PATH=/usr/local/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable \
    && rustup target add "$(uname -m)-unknown-linux-musl"

WORKDIR /src

# Dependencies first, for the layer cache.
COPY go.mod go.sum ./
RUN go mod download
COPY admin_ui/package.json admin_ui/package-lock.json admin_ui/
RUN cd admin_ui && npm ci
COPY rust/sargo/Cargo.toml rust/sargo/Cargo.lock rust/sargo/
RUN mkdir -p rust/sargo/src && echo 'fn main() {}' > rust/sargo/src/main.rs \
    && cd rust/sargo && cargo fetch --locked; rm -rf rust/sargo/src

COPY . .
RUN python3 ops/build.py all --sargo

# `docker run` copies the binaries to /out; `docker run ... build` rebuilds from /src (mount it) and then copies.
COPY <<'SH' /usr/local/bin/builder-entrypoint
#!/bin/sh
set -eu
if [ "${1:-}" = "build" ]; then
    cd /src && python3 ops/build.py all --sargo
fi
mkdir -p /out
for f in gligabao gligabao_agent gligabao_worker gligabao_agent_linux_amd64; do
    cp "/src/build/$f" /out/
done
echo "binaries copied to /out"
SH
RUN chmod +x /usr/local/bin/builder-entrypoint
ENTRYPOINT ["builder-entrypoint"]
