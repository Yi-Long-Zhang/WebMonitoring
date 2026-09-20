package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
	"webmonitoring/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		healthcheck(env("HEALTHCHECK_URL", "http://127.0.0.1:8082/readyz"))
		return
	}
	ctx := context.Background()
	logger := slog.New(security.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := config.ValidateProductionWorker(os.Getenv); err != nil {
		log.Fatalf("invalid production configuration: %v", err)
	}
	// Tracing is opt-in; a setup failure must not stop detection.
	shutdownTelemetry, err := telemetry.Setup(ctx, "webmonitoring-http-worker", os.Getenv, logger)
	if err != nil {
		logger.Warn("telemetry setup failed; continuing without tracing", "error", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer func() {
		if shutdownErr := shutdownTelemetry(ctx); shutdownErr != nil {
			logger.Warn("telemetry shutdown failed", "error", shutdownErr)
		}
	}()
	dsn := required("POSTGRES_DSN")
	repository, err := store.NewPostgresRepository(ctx, dsn, secretKeyMaterial())
	if err != nil {
		log.Fatal(err)
	}
	repository.SetJobLease(config.JobLease(os.Getenv, store.JobLease))
	defer repository.Close()
	evidenceStore, err := evidence.NewS3Store(ctx, evidence.S3Options{
		Endpoint:  required("S3_ENDPOINT"),
		AccessKey: required("S3_ACCESS_KEY"),
		SecretKey: required("S3_SECRET_KEY"),
		Bucket:    env("S3_BUCKET", "evidence"),
		Region:    os.Getenv("S3_REGION"),
	})
	if err != nil {
		log.Fatal(err)
	}
	allowPrivate, _ := strconv.ParseBool(env("ALLOW_PRIVATE_TARGETS", "false"))
	guard := security.NewURLGuard(allowPrivate)
	broker := events.NewBroker()
	workerMetrics := metrics.New()
	browserClient := browser.NewClient(os.Getenv("BROWSER_WORKER_URL"), os.Getenv("BROWSER_WORKER_TOKEN"), guard)
	var browserChecker detection.BrowserChecker
	if browserClient.Available() {
		browserChecker = browserClient
	}
	dispatcher := notify.NewDispatcher(repository, guard)
	dispatcher.SetRetryPolicy(
		int(envInt64("ALERT_MAX_ATTEMPTS", 3)),
		time.Duration(envInt64("ALERT_RETRY_BASE_MILLISECONDS", 250))*time.Millisecond,
	)
	dispatcher.SetSMTP(notify.SMTPConfig{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     config.PositiveInt(os.Getenv, "SMTP_PORT", 25),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM"),
	})
	// The consumer name must be unique per running worker: the group reclaims
	// anything another consumer has held past QUEUE_MIN_IDLE_SECONDS.
	consumer := config.WorkerIdentity(os.Getenv)
	secretCipher, err := security.NewKeyring(secretKeyMaterial())
	if err != nil {
		log.Fatal(err)
	}
	httpChecker := detection.NewChecker(guard)
	httpChecker.SetCipher(secretCipher)
	if browserClient.Available() {
		// Same fail-closed rule as the HTTP path: no cipher, no login.
		browserClient.SetCipher(secretCipher)
	}
	service := detection.NewService(
		repository,
		evidenceStore,
		httpChecker,
		browserChecker,
		dispatcher,
		broker,
		logger,
	)
	service.SetEvidenceRetention(
		time.Duration(config.PositiveInt(os.Getenv, "EVIDENCE_RETENTION_DAYS", config.DefaultEvidenceRetentionDays)) * 24 * time.Hour,
	)
	service.SetMetrics(workerMetrics)
	service.SetRetryBase(time.Duration(envInt64("QUEUE_RETRY_BASE_MILLISECONDS", 500)) * time.Millisecond)
	queue, err := jobqueue.NewRedis(ctx, required("REDIS_ADDR"), "detection-workers")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = queue.Close() }()
	queue.SetMetrics(workerMetrics)
	queue.SetMaxDeliveries(envInt64("QUEUE_MAX_DELIVERIES", 5))
	queue.SetMinIdle(time.Duration(envInt64("QUEUE_MIN_IDLE_SECONDS", 120)) * time.Second)
	queue.SetDeadLetterHandler(func(_ context.Context, payload []byte, cause error) error {
		var job domain.DetectionJob
		if err := json.Unmarshal(payload, &job); err != nil {
			logger.Error("invalid detection dead-letter payload", "error", err)
			return nil
		}
		now := time.Now().UTC()
		job.Status = domain.JobFailed
		job.ErrorCode = "DEAD_LETTER"
		job.ErrorMessage = "message exceeded the maximum delivery count"
		job.FinishedAt = &now
		if err := repository.UpdateJob(job); err != nil {
			return err
		}
		return repository.AppendAudit(domain.AuditEvent{
			ID: identity.New(), TenantID: job.TenantID, Action: "detection.dead_letter",
			ResourceType: "detection_job", ResourceID: job.ID, Result: "FAILED",
			TraceID: "worker:" + consumer, Details: map[string]any{
				"errorCode": "DEAD_LETTER", "cause": safeWorkerError(cause),
			}, CreatedAt: now,
		})
	})
	alertQueue, err := jobqueue.NewAlertRedis(ctx, required("REDIS_ADDR"), "alert-workers")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = alertQueue.Close() }()
	alertQueue.SetMetrics(workerMetrics)
	alertQueue.SetMaxDeliveries(envInt64("ALERT_QUEUE_MAX_DELIVERIES", 5))
	alertQueue.SetMinIdle(time.Duration(envInt64("ALERT_QUEUE_MIN_IDLE_SECONDS", 120)) * time.Second)
	alertQueue.SetDeadLetterHandler(func(ctx context.Context, payload []byte, cause error) error {
		var job domain.AlertJob
		if err := json.Unmarshal(payload, &job); err != nil {
			logger.Error("invalid alert dead-letter payload", "error", err)
			return nil
		}
		logger.Error("alert job moved to dead letter", "incident_id", job.IncidentID)
		if err := dispatcher.MarkDeadLetter(ctx, job); err != nil {
			return err
		}
		return repository.AppendAudit(domain.AuditEvent{
			ID: identity.New(), TenantID: job.TenantID, Action: "alert.dead_letter",
			ResourceType: "incident", ResourceID: job.IncidentID, Result: "FAILED",
			TraceID: "worker:" + consumer, Details: map[string]any{
				"errorCode": "DEAD_LETTER", "cause": safeWorkerError(cause),
			}, CreatedAt: time.Now().UTC(),
		})
	})
	service.SetAlertQueue(alertQueue)
	healthServer := &http.Server{
		Addr: env("WORKER_HTTP_ADDR", ":8082"),
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"status":"ok"}`))
				return
			}
			if request.Method == http.MethodGet && request.URL.Path == "/readyz" {
				checkContext, cancel := context.WithTimeout(request.Context(), 3*time.Second)
				defer cancel()
				checks := []func(context.Context) error{
					repository.Ready,
					evidenceStore.Ready,
					queue.Ready,
					alertQueue.Ready,
				}
				if browserClient.Available() {
					checks = append(checks, browserClient.Ready)
				}
				for _, check := range checks {
					if err := check(checkContext); err != nil {
						writer.Header().Set("Content-Type", "application/json")
						writer.WriteHeader(http.StatusServiceUnavailable)
						_, _ = writer.Write([]byte(`{"status":"not_ready"}`))
						return
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"status":"ready"}`))
				return
			}
			if request.Method == http.MethodGet && request.URL.Path == "/metrics" {
				workerMetrics.Handler().ServeHTTP(writer, request)
				return
			}
			http.NotFound(writer, request)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("worker health server stopped", "error", err)
		}
	}()
	defer healthServer.Shutdown(context.Background())
	logger.Info("detection worker started", "consumer", consumer)
	errs := make(chan error, 2)
	go func() { errs <- queue.Consume(ctx, consumer, service.Process) }()
	dispatcher.SetMetrics(workerMetrics)
	go func() {
		errs <- alertQueue.ConsumeAlerts(ctx, consumer+"-alerts", dispatcher.ProcessAlert)
	}()
	if err := <-errs; err != nil {
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

// secretKeyMaterial reads whichever variable carries the key material and fails
// closed when neither does. It reads through config.SecretKeyMaterial so the
// worker and the API cannot end up on different variables: a worker on the
// singular form would fail to decrypt what an API on the plural form wrote, and
// that only surfaces at runtime.
func secretKeyMaterial() string {
	material := config.SecretKeyMaterial(os.Getenv)
	if strings.TrimSpace(material) == "" {
		log.Fatal("ALERT_SECRET_KEY or ALERT_SECRET_KEYS must be set")
	}
	return material
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

func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(env(name, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func safeWorkerError(err error) string {
	if err == nil {
		return ""
	}
	message := security.RedactString(err.Error())
	if len(message) > 240 {
		return message[:240]
	}
	return message
}
