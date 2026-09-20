package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateProductionAPIRejectsDefaults(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://webmonitoring:webmonitoring@db:5432/webmonitoring",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "webmonitoring",
		"S3_SECRET_KEY": defaultObjectSecret, "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": defaultWorkerToken, "ALERT_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": defaultAdminPassword, "SESSION_SECURE": "false", "ALLOW_PRIVATE_TARGETS": "true",
	}
	err := ValidateProductionAPI(func(name string) string { return values[name] })
	if err == nil {
		t.Fatal("expected production defaults to be rejected")
	}
	for _, want := range []string{"ADMIN_PASSWORD", "S3_SECRET_KEY", "SESSION_SECURE", "default database password"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestValidateProductionAPIAcceptsExplicitSecrets(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://wm:strong-db-password-123@db:5432/webmonitoring",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "strong-access-key",
		"S3_SECRET_KEY": "strong-object-secret-123", "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": "strong-worker-token-123", "ALERT_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": "strong-admin-password", "SESSION_SECURE": "true", "ALLOW_PRIVATE_TARGETS": "false",
	}
	if err := ValidateProductionAPI(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// workerEnv is the production environment of the http-worker service. It omits
// ADMIN_PASSWORD, which the worker never reads, and WORKER_ID, which the worker
// generates per process so that scaled replicas do not share a consumer name.
func workerEnv() map[string]string {
	return map[string]string{
		"APP_ENV":               "production",
		"POSTGRES_DSN":          "postgres://wm:strong-db-password-123@db:5432/webmonitoring",
		"REDIS_ADDR":            "redis:6379",
		"S3_ENDPOINT":           "http://minio:9000",
		"S3_ACCESS_KEY":         "strong-access-key",
		"S3_SECRET_KEY":         "strong-object-secret-123",
		"ALERT_SECRET_KEY":      base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"BROWSER_WORKER_URL":    "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN":  "strong-worker-token-123",
		"SESSION_SECURE":        "true",
		"ALLOW_PRIVATE_TARGETS": "false",
	}
}

// validateCommon used to check ADMIN_PASSWORD for the API and the worker alike.
// The worker does not read the variable and its compose service does not set
// it, so the shared check stopped the worker tier from starting in production.
func TestValidateProductionWorkerDoesNotRequireAdminPassword(t *testing.T) {
	values := workerEnv()
	if _, present := values["ADMIN_PASSWORD"]; present {
		t.Fatal("fixture must leave ADMIN_PASSWORD unset to cover the regression")
	}
	if err := ValidateProductionWorker(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("worker must start without ADMIN_PASSWORD: %v", err)
	}
}
