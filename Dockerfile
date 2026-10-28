# syntax=docker/dockerfile:1

# Production image for the family-budget API.
#
# Image pinning policy (PLATFORM_DECISIONS §F, SECURITY_AND_OPERATIONS):
# every base image is pinned to an explicit version tag; `latest` is never used.
# Bump the pins deliberately and rerun the deployment checks.
#
# The server binary never migrates on startup (cmd/server/main.go). The schema
# authority is the Goose migration set in migrations/, applied by the one-shot
# `migrate` service built from the `migrate` stage below.

# --- builder: compile the static server binary -----------------------------
FROM golang:1.25.14-bookworm AS builder

WORKDIR /src

# Dependency layers are cached independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 keeps the runtime image free of a libc/toolchain and makes the
# result a portable static binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# --- goose-builder: compile the pinned migration runner --------------------
# Goose is built from source at a pinned module version instead of running a
# floating third-party image. The result is a static binary.
FROM golang:1.25.14-bookworm AS goose-builder

RUN CGO_ENABLED=0 GOBIN=/out go install github.com/pressly/goose/v3/cmd/goose@v3.27.3

# --- migrate: one-shot schema runner (compose service `migrate`) -----------
FROM alpine:3.24.3 AS migrate

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app -H app

COPY --from=goose-builder /out/goose /usr/local/bin/goose
COPY migrations /migrations

USER 10001:10001
WORKDIR /migrations

# DATABASE_URL is expanded at run time. CMD is appended to the shell so
# `docker compose run --rm migrate status` and friends work unchanged.
ENTRYPOINT ["/bin/sh", "-c", "exec goose -dir /migrations postgres \"$DATABASE_URL\" \"$@\"", "--"]
CMD ["up"]

# --- runtime: the API server ----------------------------------------------
# Alpine is chosen over distroless because the image needs a health-check
# utility (busybox wget) for the compose probe; ca-certificates is the only
# extra package. The server runs as the numeric non-root UID 10001.
FROM alpine:3.24.3 AS runtime

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app -H app

COPY --from=builder /out/server /server

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/server"]
