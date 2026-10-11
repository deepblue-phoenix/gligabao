# syntax=docker/dockerfile:1

# Admin UI web assets (TypeScript/SCSS -> admin_ui/dist), embedded into the binary by the Go build below.
FROM node:22-bookworm-slim AS admin_ui
WORKDIR /src/admin_ui
COPY admin_ui/package.json admin_ui/package-lock.json ./
RUN npm ci
COPY admin_ui/ ./
RUN npm run build

# Pure-Go build (no CGO): the binary is static, so the build image and a minimal runtime image need no matching libc.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=admin_ui /src/admin_ui/dist ./admin_ui/dist
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
EXPOSE 7878
# Pass ANTHROPIC_API_KEY / OPENAI_API_KEY (and PINECONE_API_KEY if vector_store = "pinecone") as environment variables.
# The SQLite database (/data/gligabao.db) and the chromem vector store (/data/gligabao.vectors/) both live in /data.
ENTRYPOINT ["/app/gligabao"]
CMD ["serve", "--db", "/data/gligabao.db"]
