package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"webmonitoring/internal/api"
	"webmonitoring/internal/auth"
	"webmonitoring/internal/browser"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/jobqueue"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

const defaultTenant = "00000000-0000-4000-8000-000000000001"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	repository, closeRepository := buildRepository(ctx, logger)
	defer closeRepository()
	seedAdmin(repository)

	dataDir := env("DATA_DIR", ".data")
	evidenceStore, err := buildEvidenceStore(ctx, dataDir)
	if err != nil {
		log.Fatal(err)
	}
	allowPrivate, _ := strconv.ParseBool(env("ALLOW_PRIVATE_TARGETS", "false"))
	guard := security.NewURLGuard(allowPrivate)
	broker := events.NewBroker()
	httpChecker := detection.NewChecker(guard)
	browserClient := browser.NewClient(os.Getenv("BROWSER_WORKER_URL"), os.Getenv("BROWSER_WORKER_TOKEN"), allowPrivate)
	var browserChecker detection.BrowserChecker
	if browserClient.Available() {
		browserChecker = browserClient
	}
	notifier := notify.NewDispatcher(repository, guard)
	detectionService := detection.NewService(
		repository, evidenceStore, httpChecker, browserChecker, notifier, broker, logger,
	)
	var closeQueue func()
	if redisAddress := os.Getenv("REDIS_ADDR"); redisAddress != "" {
		queue, queueErr := jobqueue.NewRedis(ctx, redisAddress, "detection-workers")
		if queueErr != nil {
			log.Fatal(queueErr)
		}
		detectionService.SetQueue(queue)
		closeQueue = func() { _ = queue.Close() }
	} else {
		logger.Warn("using embedded job execution; configure REDIS_ADDR for independent workers")
		closeQueue = func() {}
	}
	defer closeQueue()
	authService := auth.NewService(repository, 12*time.Hour)
	sessionSecure, _ := strconv.ParseBool(env("SESSION_SECURE", "false"))
	server := api.NewServer(
		repository, authService, detectionService, evidenceStore, guard, broker, logger, sessionSecure,
	)
	scheduler := detection.NewScheduler(repository, detectionService, logger)
	go scheduler.Run(ctx, defaultTenant)

	address := env("HTTP_ADDR", ":8080")
	logger.Info("api listening", "address", address)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func buildEvidenceStore(ctx context.Context, dataDir string) (evidence.Store, error) {
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		return evidence.NewLocalStore(filepath.Join(dataDir, "evidence"))
	}
	return evidence.NewS3Store(
		ctx,
		endpoint,
		os.Getenv("S3_ACCESS_KEY"),
		os.Getenv("S3_SECRET_KEY"),
		env("S3_BUCKET", "evidence"),
	)
}

func buildRepository(ctx context.Context, logger *slog.Logger) (store.Repository, func()) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		logger.Warn("using in-memory repository; data will be lost on restart")
		return store.NewMemoryRepository(), func() {}
	}
	repository, err := store.NewPostgresRepository(ctx, dsn, os.Getenv("ALERT_SECRET_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	logger.Info("using PostgreSQL repository")
	return repository, repository.Close
}

func seedAdmin(repository store.Repository) {
	username := env("ADMIN_USERNAME", "admin")
	password := env("ADMIN_PASSWORD", "change-me-before-production")
	if _, err := repository.FindUserByUsername(defaultTenant, username); err == nil {
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Fatal(err)
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}
	err = repository.CreateUser(domain.User{
		ID: identity.New(), TenantID: defaultTenant, Username: username,
		PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
