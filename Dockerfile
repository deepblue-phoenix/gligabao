# syntax=docker/dockerfile:1

# Pure-Go build (no CGO): the binary is static, so the build image and a minimal runtime image need no matching libc.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gligabao ./cmd/gligabao

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
# The SQLite database (/data/gligabao.db) and the chromem vector store (/data/gligabao.vectors/) both live in /data.
ENTRYPOINT ["/app/gligabao"]
CMD ["serve", "--db", "/data/gligabao.db"]
