package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strconv"

	"webmonitoring/internal/browser"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/jobqueue"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

func main() {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dsn := required("POSTGRES_DSN")
	repository, err := store.NewPostgresRepository(ctx, dsn, required("ALERT_SECRET_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	defer repository.Close()
	evidenceStore, err := evidence.NewS3Store(
		ctx,
		required("S3_ENDPOINT"),
		required("S3_ACCESS_KEY"),
		required("S3_SECRET_KEY"),
		env("S3_BUCKET", "evidence"),
	)
	if err != nil {
		log.Fatal(err)
	}
	allowPrivate, _ := strconv.ParseBool(env("ALLOW_PRIVATE_TARGETS", "false"))
	guard := security.NewURLGuard(allowPrivate)
	broker := events.NewBroker()
	browserClient := browser.NewClient(os.Getenv("BROWSER_WORKER_URL"), os.Getenv("BROWSER_WORKER_TOKEN"))
	var browserChecker detection.BrowserChecker
	if browserClient.Available() {
		browserChecker = browserClient
	}
	service := detection.NewService(
		repository,
		evidenceStore,
		detection.NewChecker(guard),
		browserChecker,
		notify.NewDispatcher(repository, guard),
		broker,
		logger,
	)
	queue, err := jobqueue.NewRedis(ctx, required("REDIS_ADDR"), "detection-workers")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	consumer := env("WORKER_ID", "worker-1")
	logger.Info("detection worker started", "consumer", consumer)
	if err := queue.Consume(ctx, consumer, service.Process); err != nil {
		log.Fatal(err)
	}
}

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
