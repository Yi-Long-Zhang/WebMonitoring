FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/migrate ./apps/migrate
COPY db/migrations ./db/migrations
COPY internal/migrate ./internal/migrate
# The build identity the /api/v1/version endpoint reports. Defaults are the
# truth for an image built without them, which is what a local `docker compose
# build` produces.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X webmonitoring/internal/version.Version=${VERSION} -X webmonitoring/internal/version.Commit=${COMMIT} -X webmonitoring/internal/version.BuildTime=${BUILD_TIME}" -o /out/webmonitoring-migrate ./apps/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-migrate /app/webmonitoring-migrate
ENTRYPOINT ["/app/webmonitoring-migrate"]
