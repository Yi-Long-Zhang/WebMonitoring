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
	"webmonitoring/internal/config"
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
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		healthcheck(env("HEALTHCHECK_URL", "http://127.0.0.1:8080/readyz"))
		return
	}
	logger := slog.New(security.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := config.ValidateProductionAPI(os.Getenv); err != nil {
		log.Fatalf("invalid production configuration: %v", err)
	}
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
	notifier.SetRetryPolicy(
		int(envInt64("ALERT_MAX_ATTEMPTS", 3)),
		time.Duration(envInt64("ALERT_RETRY_BASE_MILLISECONDS", 250))*time.Millisecond,
	)
	detectionService := detection.NewService(
		repository, evidenceStore, httpChecker, browserChecker, notifier, broker, logger,
	)
	detectionService.SetEvidenceRetention(
		time.Duration(config.PositiveInt(os.Getenv, "EVIDENCE_RETENTION_DAYS", config.DefaultEvidenceRetentionDays)) * 24 * time.Hour,
	)
	detectionService.SetRetryBase(time.Duration(envInt64("QUEUE_RETRY_BASE_MILLISECONDS", 500)) * time.Millisecond)
	var closeQueue func()
	var queueReady interface{ Ready(context.Context) error }
	var detectionQueue *jobqueue.Redis
	var alertQueue *jobqueue.Redis
	if redisAddress := os.Getenv("REDIS_ADDR"); redisAddress != "" {
		queue, queueErr := jobqueue.NewRedis(ctx, redisAddress, "detection-workers")
		if queueErr != nil {
			log.Fatal(queueErr)
		}
		detectionService.SetQueue(queue)
		detectionQueue = queue
		queueReady = queue
		alertQueue, queueErr = jobqueue.NewAlertRedis(ctx, redisAddress, "alert-workers")
		if queueErr != nil {
			log.Fatal(queueErr)
		}
		detectionService.SetAlertQueue(alertQueue)
		queue.SetMaxDeliveries(envInt64("QUEUE_MAX_DELIVERIES", 5))
		queue.SetMinIdle(time.Duration(envInt64("QUEUE_MIN_IDLE_SECONDS", 120)) * time.Second)
		alertQueue.SetMaxDeliveries(envInt64("ALERT_QUEUE_MAX_DELIVERIES", 5))
		alertQueue.SetMinIdle(time.Duration(envInt64("ALERT_QUEUE_MIN_IDLE_SECONDS", 120)) * time.Second)
		closeQueue = func() {
			_ = queue.Close()
			_ = alertQueue.Close()
		}
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
	server.SetRetentionPolicy(
		config.PositiveInt(os.Getenv, "EVIDENCE_RETENTION_DAYS", config.DefaultEvidenceRetentionDays),
		config.PositiveInt(os.Getenv, "AUDIT_RETENTION_DAYS", config.DefaultAuditRetentionDays),
	)
	detectionService.SetMetrics(server.Metrics())
	notifier.SetMetrics(server.Metrics())
	if detectionQueue != nil {
		detectionQueue.SetMetrics(server.Metrics())
	}
	if alertQueue != nil {
		alertQueue.SetMetrics(server.Metrics())
	}
	server.SetReadiness(func(checkContext context.Context) map[string]error {
		failures := make(map[string]error)
		if checker, ok := repository.(interface{ Ready(context.Context) error }); ok {
			if err := checker.Ready(checkContext); err != nil {
				failures["postgres"] = err
			}
		}
		if checker, ok := evidenceStore.(evidence.HealthChecker); ok {
			if err := checker.Ready(checkContext); err != nil {
				failures["evidence"] = err
			}
		}
		if browserClient.Available() {
			if err := browserClient.Ready(checkContext); err != nil {
				failures["browser_worker"] = err
			}
		}
		if queueReady != nil {
			if err := queueReady.Ready(checkContext); err != nil {
				failures["redis"] = err
			}
		}
		if alertQueue != nil {
			if err := alertQueue.Ready(checkContext); err != nil {
				failures["alert_redis"] = err
			}
		}
		return failures
	})
	scheduler := detection.NewScheduler(repository, detectionService, logger)
	go scheduler.Run(ctx)

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

func healthcheck(target string) {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		log.Fatalf("health check returned %d", response.StatusCode)
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
		PasswordHash: hash, Role: domain.RoleSuperAdmin, CreatedAt: time.Now().UTC(),
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

func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(env(name, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
