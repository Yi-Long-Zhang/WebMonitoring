package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/notifygate"
	"webmonitoring/internal/ratelimit"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
	"webmonitoring/internal/telemetry"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Tracing is opt-in. A failure to set it up is logged and swallowed: the
	// control plane must start even when the collector is unreachable.
	shutdownTelemetry, err := telemetry.Setup(ctx, "webmonitoring-api", os.Getenv, logger)
	if err != nil {
		logger.Warn("telemetry setup failed; continuing without tracing", "error", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer func() {
		if shutdownErr := shutdownTelemetry(context.Background()); shutdownErr != nil {
			logger.Warn("telemetry shutdown failed", "error", shutdownErr)
		}
	}()
	repository, closeRepository := buildRepository(ctx, logger)
	defer closeRepository()
	seedAdmin(ctx, repository)

	dataDir := env("DATA_DIR", ".data")
	evidenceStore, err := buildEvidenceStore(ctx, dataDir)
	if err != nil {
		log.Fatal(err)
	}
	allowPrivate, _ := strconv.ParseBool(env("ALLOW_PRIVATE_TARGETS", "false"))
	guard := security.NewURLGuard(allowPrivate)
	broker := events.NewBroker()
	var secretCipher security.SecretBox
	if secret := config.SecretKeyMaterial(os.Getenv); secret != "" {
		if cipher, cipherErr := security.NewKeyring(secret); cipherErr == nil {
			secretCipher = cipher
		} else {
			logger.Warn("invalid ALERT_SECRET_KEY; login-state detection disabled", "error", cipherErr)
		}
	}
	httpChecker := detection.NewChecker(guard)
	if secretCipher != nil {
		httpChecker.SetCipher(secretCipher)
	}
	browserClient := browser.NewClient(os.Getenv("BROWSER_WORKER_URL"), os.Getenv("BROWSER_WORKER_TOKEN"), guard)
	var browserChecker detection.BrowserChecker
	if browserClient.Available() {
		// Login-state detection needs the cipher on this path too; without it
		// the client fails closed on any policy carrying an Auth config.
		if secretCipher != nil {
			browserClient.SetCipher(secretCipher)
		}
		browserChecker = browserClient
	}
	notifier := notify.NewDispatcher(repository, guard)
	notifier.SetRetryPolicy(
		int(envInt64("ALERT_MAX_ATTEMPTS", 3)),
		time.Duration(envInt64("ALERT_RETRY_BASE_MILLISECONDS", 250))*time.Millisecond,
	)
	notifier.SetSMTP(notify.SMTPConfig{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     config.PositiveInt(os.Getenv, "SMTP_PORT", 25),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM"),
	})
	// Notifications are normally delivered by the alert worker, which installs
	// its own gate. This one covers the embedded mode, where there is no worker
	// and the API sends the alert itself -- and the transition and comment
	// notifications the API raises directly. Sharing a Redis-backed gate is what
	// makes the two agree when both are running.
	alertPolicy := notifygate.ParsePolicy(os.Getenv,
		time.Duration(config.PositiveInt(os.Getenv, "ALERT_GATE_WINDOW_SECONDS", 60))*time.Second)
	if redisAddress := os.Getenv("REDIS_ADDR"); redisAddress != "" {
		notifier.SetGate(notifygate.NewRedisGate(redisAddress, alertPolicy))
	} else {
		notifier.SetGate(notifygate.NewMemoryGate(alertPolicy))
	}
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
	if redisAddress := os.Getenv("REDIS_ADDR"); redisAddress != "" {
		authService.SetSessionStore(auth.NewRedisSessionStore(redisAddress))
	}
	loginMaxFailures := config.PositiveInt(os.Getenv, "LOGIN_MAX_FAILURES", 5)
	loginLockout := time.Duration(config.PositiveInt(os.Getenv, "LOGIN_LOCKOUT_MINUTES", 15)) * time.Minute
	var loginGuard *auth.LoginGuard
	if os.Getenv("REDIS_ADDR") != "" {
		loginGuard = auth.NewRedisLoginGuard(os.Getenv("REDIS_ADDR"), loginMaxFailures, loginLockout)
	} else {
		loginGuard = auth.NewMemoryLoginGuard(loginMaxFailures, loginLockout)
	}
	sessionSecure, _ := strconv.ParseBool(env("SESSION_SECURE", "false"))
	server := api.NewServer(
		repository, authService, detectionService, evidenceStore, guard, broker, logger, sessionSecure,
	)
	server.SetLoginGuard(loginGuard)
	server.SetAllowedOrigins(config.AllowedOrigins(os.Getenv))
	// The request allowance is shared through Redis when there is one, so the
	// effective limit does not scale with the number of replicas. Without Redis
	// the in-process limiter is used, which is correct for the single-process
	// embedded mode and is what the integration tests run against.
	rateLimits := ratelimit.ParseLimits(os.Getenv, time.Duration(config.PositiveInt(os.Getenv, "RATE_LIMIT_WINDOW_SECONDS", 60))*time.Second)
	if redisAddress := os.Getenv("REDIS_ADDR"); redisAddress != "" {
		server.SetRateLimiter(ratelimit.NewRedisLimiter(redisAddress, rateLimits))
	} else {
		server.SetRateLimiter(ratelimit.NewMemoryLimiter(rateLimits))
	}
	if secretCipher != nil {
		server.SetSecretCipher(secretCipher)
	}
	server.SetRetentionPolicy(
		config.PositiveInt(os.Getenv, "EVIDENCE_RETENTION_DAYS", config.DefaultEvidenceRetentionDays),
		config.PositiveInt(os.Getenv, "AUDIT_RETENTION_DAYS", config.DefaultAuditRetentionDays),
		config.PositiveInt(os.Getenv, "JOB_RETENTION_DAYS", config.DefaultJobRetentionDays),
	)
	if keyring, ok := secretCipher.(*security.Keyring); ok {
		if signer, err := security.NewSignedURLSigner(keyring.PrimaryKeyMaterial()); err == nil {
			server.SetSignedURLs(signer, config.PositiveInt(os.Getenv, "EVIDENCE_SIGNED_URL_TTL_SECONDS", 300))
		}
	}
	detectionService.SetMetrics(server.Metrics())
	notifier.SetMetrics(server.Metrics())
	if detectionQueue != nil {
		detectionQueue.SetMetrics(server.Metrics())
	}
	if alertQueue != nil {
		alertQueue.SetMetrics(server.Metrics())
	}
	// The pool counters are read per scrape rather than sampled on a ticker: the
	// number that matters is how full the pool is when someone asks, and a
	// sampler would report the pool as it was at the last tick.
	if postgres, ok := repository.(*store.PostgresRepository); ok {
		server.Metrics().OnCollect(func(registry *metrics.Registry) {
			stats := postgres.PoolStats()
			registry.Set("webmonitoring_postgres_pool_connections", float64(stats.TotalConns()), map[string]string{"state": "total"})
			registry.Set("webmonitoring_postgres_pool_connections", float64(stats.AcquiredConns()), map[string]string{"state": "acquired"})
			registry.Set("webmonitoring_postgres_pool_connections", float64(stats.IdleConns()), map[string]string{"state": "idle"})
			registry.Set("webmonitoring_postgres_pool_connections", float64(stats.ConstructingConns()), map[string]string{"state": "constructing"})
			registry.Set("webmonitoring_postgres_pool_max_connections", float64(stats.MaxConns()), nil)
		})
	}
	// The open-incident gauges are aggregate queries run at scrape time. The
	// previous implementation counted whatever page of the incident list the
	// console had last requested, so the gauges silently saturated at the page
	// size (50 by default) once an installation held more open incidents.
	server.Metrics().OnCollect(func(registry *metrics.Registry) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		open, highRisk, critical, err := repository.CountOpenIncidents(ctx)
		if err != nil {
			logger.Warn("collect incident gauges failed; keeping previous values", "error", err)
			return
		}
		registry.Set("webmonitoring_open_incidents", float64(open), nil)
		registry.Set("webmonitoring_high_risk_incidents", float64(highRisk), nil)
		registry.Set("webmonitoring_critical_incidents", float64(critical), nil)
	})
	server.SetMetricsToken(env("METRICS_TOKEN", ""))
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

	// Retention runs in the control plane rather than every worker, so evidence
	// is swept once per interval regardless of how many workers are running.
	// A shared lease narrows that to once per interval across all replicas: the
	// sweep runs immediately at startup, so without it every replica restart
	// would start its own full pass. The in-memory repository is not a
	// ReplicaLease and needs none, being a single process.
	if lease, ok := repository.(store.ReplicaLease); ok {
		server.RetentionRunner().SetLease(lease)
	}
	if interval := time.Duration(envInt64("RETENTION_INTERVAL_HOURS", 24)) * time.Hour; interval > 0 {
		go server.RetentionRunner().RunDaily(ctx, interval)
	} else {
		logger.Warn("background retention disabled; only the manual endpoint will run it")
	}

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
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("api server stopped", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	logger.Info("api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
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
	return evidence.NewS3Store(ctx, evidence.S3Options{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Bucket:    env("S3_BUCKET", "evidence"),
		Region:    os.Getenv("S3_REGION"),
	})
}

func buildRepository(ctx context.Context, logger *slog.Logger) (store.Repository, func()) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		logger.Warn("using in-memory repository; data will be lost on restart")
		return store.NewMemoryRepository(), func() {}
	}
	repository, err := store.NewPostgresRepository(ctx, dsn, config.SecretKeyMaterial(os.Getenv), store.PoolConfigFromEnv(os.Getenv))
	if err != nil {
		log.Fatal(err)
	}
	repository.SetJobLease(config.JobLease(os.Getenv, store.JobLease))
	logger.Info("using PostgreSQL repository")
	return repository, repository.Close
}

func seedAdmin(ctx context.Context, repository store.Repository) {
	username := env("ADMIN_USERNAME", "admin")
	// No compiled-in fallback: the development default lives in
	// deployments/compose/.env.default so the binary carries no password, and a
	// deployment changes it by editing that file or setting ADMIN_PASSWORD.
	password := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
	if password == "" {
		password = config.DefaultAdminPassword(os.Getenv)
	}
	if password == "" {
		log.Fatal("ADMIN_PASSWORD is not set and no default password file was found; " +
			"set ADMIN_PASSWORD, or provide deployments/compose/.env.default")
	}
	if _, err := repository.FindUserByUsername(ctx, defaultTenant, username); err == nil {
		// Already seeded. Returning here is what keeps a restart from resetting
		// a password the operator has since changed.
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Fatal(err)
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}
	err = repository.CreateUser(ctx, domain.User{
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
