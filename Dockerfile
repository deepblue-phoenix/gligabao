# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gligabao .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 gligabao && mkdir /data && chown gligabao /data
WORKDIR /app
COPY --from=build /out/gligabao /app/gligabao
# Config is read from next to the binary by default.
COPY bin/gligabao.toml /app/gligabao.toml
USER gligabao
VOLUME /data
EXPOSE 8080
# Pass ANTHROPIC_API_KEY / OPENAI_API_KEY as environment variables.
ENTRYPOINT ["/app/gligabao"]
CMD ["serve", "--db", "/data/gligabao.db"]
