FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/migrate ./apps/migrate
COPY db/migrations ./db/migrations
COPY internal/migrate ./internal/migrate
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webmonitoring-migrate ./apps/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-migrate /app/webmonitoring-migrate
ENTRYPOINT ["/app/webmonitoring-migrate"]
