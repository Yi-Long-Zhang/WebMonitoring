package config

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePostgresPoolRejectsUnboundedAndInconsistentBounds(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		// Unset means the built-in default, which is a real bound.
		{"nothing configured", map[string]string{}, ""},
		{"valid bounds", map[string]string{"POSTGRES_MAX_CONNS": "25", "POSTGRES_MIN_CONNS": "2"}, ""},
		{"min equal to max", map[string]string{"POSTGRES_MAX_CONNS": "10", "POSTGRES_MIN_CONNS": "10"}, ""},
		// Zero is the value that leaves the pool on pgxpool's own default, so it
		// is refused rather than read as "the default I meant".
		{"zero max", map[string]string{"POSTGRES_MAX_CONNS": "0"}, "POSTGRES_MAX_CONNS must be positive"},
		{"negative max", map[string]string{"POSTGRES_MAX_CONNS": "-1"}, "POSTGRES_MAX_CONNS must be positive"},
		{"ceiling", map[string]string{"POSTGRES_MAX_CONNS": "5000"}, "must not exceed"},
		{"min above max", map[string]string{"POSTGRES_MAX_CONNS": "10", "POSTGRES_MIN_CONNS": "20"}, "must not exceed POSTGRES_MAX_CONNS"},
		{"negative min", map[string]string{"POSTGRES_MIN_CONNS": "-1"}, "must not be negative"},
		{"unparseable max", map[string]string{"POSTGRES_MAX_CONNS": "lots"}, "must be an integer"},
		{"zero lifetime", map[string]string{"POSTGRES_MAX_CONN_LIFETIME_SECONDS": "0"}, "positive number of seconds"},
		{"unparseable connect timeout", map[string]string{"POSTGRES_CONNECT_TIMEOUT_SECONDS": "soon"}, "positive number of seconds"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failures := errors.Join(validatePostgresPool(envFrom(testCase.values))...)
			switch {
			case testCase.want == "" && failures != nil:
				t.Fatalf("unexpected failure: %v", failures)
			case testCase.want != "" && failures == nil:
				t.Fatalf("expected a failure containing %q, got none", testCase.want)
			case testCase.want != "" && !strings.Contains(failures.Error(), testCase.want):
				t.Fatalf("error %q does not contain %q", failures, testCase.want)
			}
		})
	}
}

// The gate has to run for the worker too: it opens its own pool, so a bound that
// is refused for the API but accepted for the worker would leave the hole open.
func TestValidateProductionWorkerAppliesPoolBounds(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_MAX_CONNS": "0", "SESSION_SECURE": "true",
		"ALLOW_PRIVATE_TARGETS": "false",
		"POSTGRES_DSN":          "postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=require",
		"REDIS_ADDR":            "redis:6379", "S3_ENDPOINT": "http://minio:9000",
		"S3_ACCESS_KEY": "strong-access-key", "S3_SECRET_KEY": "strong-object-secret-123",
		"BROWSER_WORKER_URL": "http://browser-worker:8081", "BROWSER_WORKER_TOKEN": "strong-worker-token-123",
	}
	err := ValidateProductionWorker(envFrom(values))
	if err == nil || !strings.Contains(err.Error(), "POSTGRES_MAX_CONNS") {
		t.Fatalf("worker did not reject an unbounded pool: %v", err)
	}
}
