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
COPY build/gligabao.toml /app/gligabao.toml
# The evals of trained models (static definitions), read from disk.
COPY build/evals_training /app/evals_training
# The ontology files that can be loaded by name (POST /v1/gb/ontologies with "file"; ontology_dirs of the config).
COPY build/ontology /app/ontology
USER gligabao
VOLUME /data
EXPOSE 7878
# Pass ANTHROPIC_API_KEY / OPENAI_API_KEY (and PINECONE_API_KEY if vector_store = "pinecone") as environment variables.
# The SQLite database (/data/gligabao.db), the chromem vector store (/data/gligabao.vectors/) and the image files
# (/data/gligabao.images/) all live in /data, and so does the training datasets directory (/data/datasets_training/: one
# subdirectory per dataset, with a dataset.toml manifest): --training-dataset-dir overrides datasets_training_dirs of the config,
# which would resolve to /app/datasets_training, not writable here.
ENTRYPOINT ["/app/gligabao"]
CMD ["serve", "--db", "/data/gligabao.db", "--training-dataset-dir", "/data/datasets_training", "--training-eval-dir", "/app/evals_training"]
