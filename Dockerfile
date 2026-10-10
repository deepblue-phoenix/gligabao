# syntax=docker/dockerfile:1

# Debian (glibc) rather than alpine: sqlite-vec's C source doesn't compile against musl.
# The golang image already ships gcc. The runtime image must have the same libc as the build one.
FROM golang:1.26-bookworm AS build
SHELL ["/bin/bash", "-c"]
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO build (sqlite-vec); cgo_env.sh enables CGO and adds the sqlite3.h include path.
RUN source ./cgo_env.sh && go build -trimpath -ldflags="-s -w" -o /out/gligabao ./cmd/gligabao

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home gligabao && mkdir /data && chown gligabao /data
WORKDIR /app
COPY --from=build /out/gligabao /app/gligabao
# Config is read from next to the binary by default.
COPY bin/gligabao.toml /app/gligabao.toml
USER gligabao
VOLUME /data
EXPOSE 8080
# Pass ANTHROPIC_API_KEY / OPENAI_API_KEY (and PINECONE_API_KEY if vector_store = "pinecone") as environment variables.
ENTRYPOINT ["/app/gligabao"]
CMD ["serve", "--db", "/data/gligabao.db"]
