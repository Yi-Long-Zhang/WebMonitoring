# Runs the key-rotation pass. Built like the migrate image: a static binary in a
# distroless image, because this is an operator command that should be launched
# into the Compose network rather than installed on a host.
FROM golang:1.27-alpine AS build
# The official image ships GOTOOLCHAIN=local, which makes the go.mod
# `toolchain` line a no-op: the build would silently use the image's patch
# level and the deployed binary would carry whatever standard-library
# advisories that patch has. auto makes go.mod the single source of truth,
# so the container and every developer resolve the same floor.
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
# Unset by default, which leaves the toolchain's own proxy. A builder behind a
# mirror, or on a network that blocks proxy.golang.org, sets it:
#   docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
# Only written when provided, so an empty value cannot be mistaken for "off".
ARG GOPROXY
RUN if [ -n "$GOPROXY" ]; then go env -w GOPROXY="$GOPROXY"; fi && go mod download
COPY apps/rewrap ./apps/rewrap
COPY internal/rewrap ./internal/rewrap
COPY internal/security ./internal/security
COPY internal/store ./internal/store
COPY internal/domain ./internal/domain
COPY internal/identity ./internal/identity
COPY internal/config ./internal/config
# This course of COPY lines is the build inputs of this binary, and every
# internal package main.go imports has to appear in it: `go build` sees only
# what was copied, so a missing line fails the build with
#   apps/rewrap/main.go:32:2: package webmonitoring/internal/version is not in std
# which is exactly what happened on 2026-10-12, when main.go started importing
# internal/version to log the build identity.
COPY internal/version ./internal/version
# The build identity the binary logs before it touches customer data (these
# images have no HTTP endpoint to report it on). Defaults are the truth for an
# image built without them, which is what a local `docker compose build`
# produces.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X webmonitoring/internal/version.Version=${VERSION} -X webmonitoring/internal/version.Commit=${COMMIT} -X webmonitoring/internal/version.BuildTime=${BUILD_TIME}" -o /out/webmonitoring-rewrap ./apps/rewrap

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-rewrap /app/webmonitoring-rewrap
ENTRYPOINT ["/app/webmonitoring-rewrap"]
