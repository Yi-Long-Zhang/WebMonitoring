FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/http-worker ./apps/http-worker
COPY internal ./internal
# The build identity the /api/v1/version endpoint reports. Defaults are the
# truth for an image built without them, which is what a local `docker compose
# build` produces.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X webmonitoring/internal/version.Version=${VERSION} -X webmonitoring/internal/version.Commit=${COMMIT} -X webmonitoring/internal/version.BuildTime=${BUILD_TIME}" -o /out/webmonitoring-worker ./apps/http-worker

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-worker /app/webmonitoring-worker
EXPOSE 8082
HEALTHCHECK --interval=10s --timeout=5s --retries=12 CMD ["/app/webmonitoring-worker", "--healthcheck"]
ENTRYPOINT ["/app/webmonitoring-worker"]
