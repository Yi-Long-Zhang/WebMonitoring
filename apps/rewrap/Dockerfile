# Runs the key-rotation pass. Built like the migrate image: a static binary in a
# distroless image, because this is an operator command that should be launched
# into the Compose network rather than installed on a host.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/rewrap ./apps/rewrap
COPY internal/rewrap ./internal/rewrap
COPY internal/security ./internal/security
COPY internal/store ./internal/store
COPY internal/domain ./internal/domain
COPY internal/identity ./internal/identity
COPY internal/config ./internal/config
# The build identity the /api/v1/version endpoint reports. Defaults are the
# truth for an image built without them, which is what a local `docker compose
# build` produces.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X webmonitoring/internal/version.Version=${VERSION} -X webmonitoring/internal/version.Commit=${COMMIT} -X webmonitoring/internal/version.BuildTime=${BUILD_TIME}" -o /out/webmonitoring-rewrap ./apps/rewrap

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-rewrap /app/webmonitoring-rewrap
ENTRYPOINT ["/app/webmonitoring-rewrap"]
