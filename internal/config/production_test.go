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
