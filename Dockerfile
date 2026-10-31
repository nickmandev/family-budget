# syntax=docker/dockerfile:1

# Production image for the family-budget API.
#
# Image pinning policy (PLATFORM_DECISIONS §F, SECURITY_AND_OPERATIONS):
# every base image is pinned to an explicit version tag; `latest` is never used.
# Bump the pins deliberately and rerun the deployment checks.
#
# The server binary never migrates on startup (cmd/server/main.go). The schema
# authority is the Goose migration set in migrations/, applied by the one-shot
# `migrate` service built from the `migrate` stage below. Both binaries share
# the same Go DSN helper (internal/config), so no shell constructs a URL.

# --- builder: compile the static server binary -----------------------------
FROM golang:1.25.14-bookworm AS builder

WORKDIR /src

# Dependency layers are cached independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 keeps the runtime image free of a libc/toolchain and makes the
# result a portable static binary. The migrate entrypoint is built here too so
# it imports the same pinned Goose library and the shared DSN helper.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# --- migrate: one-shot schema runner (compose service `migrate`) -----------
# Goose is a pinned module dependency (go.mod), not a floating binary/image.
FROM alpine:3.24.3 AS migrate

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app -H app

COPY --from=builder /out/migrate /migrate
COPY migrations /migrations

USER 10001:10001
WORKDIR /migrations

# The DSN is constructed inside the binary from POSTGRES_* components. Extra
# arguments (for example `status` or `down`) are appended to the entrypoint, so
# `docker compose run --rm migrate status` works unchanged.
ENTRYPOINT ["/migrate", "-dir", "/migrations"]
CMD ["up"]

# --- runtime: the API server ----------------------------------------------
# Alpine is chosen over distroless because the image needs a health-check
# utility (busybox wget) for the compose probe; ca-certificates and tzdata are
# installed for TLS and timezone support. The server runs as the numeric
# non-root UID 10001.
FROM alpine:3.24.3 AS runtime

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app -H app

COPY --from=builder /out/server /server

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/server"]
