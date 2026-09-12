FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps/api ./apps/api
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webmonitoring-api ./apps/api

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/webmonitoring-api /app/webmonitoring-api
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=12 CMD ["/app/webmonitoring-api", "--healthcheck"]
ENTRYPOINT ["/app/webmonitoring-api"]
