package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestValidateProductionAPIRejectsDefaults(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://webmonitoring:webmonitoring@db:5432/webmonitoring",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "webmonitoring",
		"S3_SECRET_KEY": defaultObjectSecret, "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": defaultWorkerToken, "ALERT_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": "short", "SESSION_SECURE": "false", "ALLOW_PRIVATE_TARGETS": "true",
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

// The published development default is refused, and the baseline it is compared
// against comes from the file rather than from a constant in this package --
// otherwise the check would put the password back into the source it was moved
// out of.
func TestValidateProductionAPIRejectsPublishedAdminDefault(t *testing.T) {
	published := DefaultAdminPassword(os.Getenv)
	if published == "" {
		t.Skip("no default password file present; nothing to compare against")
	}
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=require",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "strong-access-key",
		"S3_SECRET_KEY": "strong-object-secret-123", "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": "strong-worker-token-123", "ALERT_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": published, "SESSION_SECURE": "true", "ALLOW_PRIVATE_TARGETS": "false",
	}
	err := ValidateProductionAPI(func(name string) string { return values[name] })
	if err == nil {
		t.Fatalf("expected %q to be rejected in production", published)
	}
	if !strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Fatalf("error %q does not name ADMIN_PASSWORD", err)
	}
}

func TestValidateProductionAPIAcceptsExplicitSecrets(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=require",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "strong-access-key",
		"S3_SECRET_KEY": "strong-object-secret-123", "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": "strong-worker-token-123", "ALERT_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"ADMIN_PASSWORD": "strong-admin-password", "SESSION_SECURE": "true", "ALLOW_PRIVATE_TARGETS": "false",
		"METRICS_TOKEN": "strong-metrics-token-0123456",
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
		"POSTGRES_DSN":          "postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=require",
		"REDIS_ADDR":            "redis:6379",
		"S3_ENDPOINT":           "http://minio:9000",
		"S3_ACCESS_KEY":         "strong-access-key",
		"S3_SECRET_KEY":         "strong-object-secret-123",
		"ALERT_SECRET_KEY":      base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"BROWSER_WORKER_URL":    "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN":  "strong-worker-token-123",
		"SESSION_SECURE":        "true",
		"ALLOW_PRIVATE_TARGETS": "false",
		"METRICS_TOKEN":         "strong-metrics-token-0123456",
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

// ADR-0004 sends decrypted customer credentials to this endpoint, so plain http
// is only acceptable where the host cannot be a public name.
func TestValidateProductionWorkerEndpointTransport(t *testing.T) {
	cases := map[string]struct {
		url     string
		rejects bool
	}{
		"https domain":       {"https://browser.example.com", false},
		"https service name": {"https://browser-worker", false},
		"http service name":  {"http://browser-worker:8081", false},
		"http private ip":    {"http://10.1.2.3:8081", false},
		"http loopback":      {"http://127.0.0.1:8081", false},
		"http domain":        {"http://browser.example.com", true},
		"http public ip":     {"http://203.0.113.10", true},
		"unsupported scheme": {"ftp://browser-worker", true},
	}
	for name, testCase := range cases {
		values := workerEnv()
		values["BROWSER_WORKER_URL"] = testCase.url
		err := ValidateProductionWorker(func(field string) string { return values[field] })
		if testCase.rejects && err == nil {
			t.Errorf("%s: %s was accepted", name, testCase.url)
		}
		if !testCase.rejects && err != nil {
			t.Errorf("%s: %s was rejected: %v", name, testCase.url, err)
		}
	}
}

// productionEnvWithSecrets is the accepted production environment, so each case
// below varies only the key material.
func productionEnvWithSecrets() map[string]string {
	return map[string]string{
		"APP_ENV": "production", "POSTGRES_DSN": "postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=require",
		"REDIS_ADDR": "redis:6379", "S3_ENDPOINT": "http://minio:9000", "S3_ACCESS_KEY": "strong-access-key",
		"S3_SECRET_KEY": "strong-object-secret-123", "BROWSER_WORKER_URL": "http://browser-worker:8081",
		"BROWSER_WORKER_TOKEN": "strong-worker-token-123",
		"ADMIN_PASSWORD":       "strong-admin-password", "SESSION_SECURE": "true", "ALLOW_PRIVATE_TARGETS": "false",
		"METRICS_TOKEN": "strong-metrics-token-0123456",
	}
}

func keyMaterial(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// A deployment that migrated to the versioned variable must not be rejected for
// having dropped the singular one, and the versioned form is what makes a key
// rotation possible at all.
func TestValidateProductionAcceptsVersionedKeys(t *testing.T) {
	values := productionEnvWithSecrets()
	values["ALERT_SECRET_KEYS"] = "v1:" + keyMaterial(1) + ",v2:" + keyMaterial(2)
	if err := ValidateProductionAPI(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("versioned key material was rejected: %v", err)
	}

	// The singular form keeps working unchanged.
	values = productionEnvWithSecrets()
	values["ALERT_SECRET_KEY"] = keyMaterial(1)
	if err := ValidateProductionAPI(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("singular key material was rejected: %v", err)
	}
}

func TestValidateProductionRejectsMalformedKeyMaterial(t *testing.T) {
	cases := []struct {
		name     string
		material string
		want     string
	}{
		{"unlabelled entry in the plural form", keyMaterial(1) + ",v2:" + keyMaterial(2), "must carry a version label"},
		{"duplicate version", "v1:" + keyMaterial(1) + ",v1:" + keyMaterial(2), "more than once"},
		{"key of the wrong length", "v1:" + base64.StdEncoding.EncodeToString([]byte("short")), "32-byte"},
		{"zero version", "v0:" + keyMaterial(1), "must carry a version label"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := productionEnvWithSecrets()
			values["ALERT_SECRET_KEYS"] = testCase.material
			err := ValidateProductionAPI(func(name string) string { return values[name] })
			if err == nil {
				t.Fatal("malformed key material was accepted")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not contain %q", err, testCase.want)
			}
		})
	}
}

// With neither variable set the cipher has no key, so login-state detection and
// alert channel credentials would silently stop working.
func TestValidateProductionRequiresKeyMaterial(t *testing.T) {
	values := productionEnvWithSecrets()
	err := ValidateProductionAPI(func(name string) string { return values[name] })
	if err == nil {
		t.Fatal("a production environment with no key material was accepted")
	}
	if !strings.Contains(err.Error(), "ALERT_SECRET_KEY or ALERT_SECRET_KEYS") {
		t.Fatalf("error does not name both variables: %v", err)
	}
}

// Transport encryption is enforced, not merely documented: a production
// deployment left on sslmode=disable fails to start, naming the variable that
// carries the setting.
func TestValidateProductionRejectsUnencryptedDatabaseTransport(t *testing.T) {
	for _, dsn := range []string{
		"postgres://wm:strong-db-password-123@db:5432/webmonitoring",
		"postgres://wm:strong-db-password-123@db:5432/webmonitoring?sslmode=disable",
	} {
		values := productionEnvWithSecrets()
		values["ALERT_SECRET_KEY"] = keyMaterial(1)
		values["POSTGRES_DSN"] = dsn
		err := ValidateProductionAPI(func(name string) string { return values[name] })
		if err == nil {
			t.Fatalf("DSN %q was accepted", dsn)
		}
		if !strings.Contains(err.Error(), "sslmode") || !strings.Contains(err.Error(), "POSTGRES_SSLMODE") {
			t.Fatalf("error does not name the setting: %v", err)
		}
	}
}

// A Redis password is only required when the address carries one, so the bare
// host:port form the integration tests and a single-node dev stack use keeps
// working. Once a deployment opts in, a weak password is refused.
func TestValidateProductionRedisPassword(t *testing.T) {
	cases := []struct {
		address string
		wantErr bool
	}{
		{"redis:6379", false},
		{"redis://:a-strong-redis-password@redis:6379/0", false},
		{"rediss://:a-strong-redis-password@redis:6379/0", false},
		{"redis://:webmonitoring@redis:6379/0", true},
		{"redis://:short@redis:6379/0", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.address, func(t *testing.T) {
			values := productionEnvWithSecrets()
			values["ALERT_SECRET_KEY"] = keyMaterial(1)
			values["REDIS_ADDR"] = testCase.address
			err := ValidateProductionAPI(func(name string) string { return values[name] })
			if testCase.wantErr && err == nil {
				t.Fatal("weak redis credentials were accepted")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

// The parser is shared by the two job queues, the session store and the login
// limiter, so its two accepted forms are pinned here.
func TestParseRedisAddress(t *testing.T) {
	cases := []struct {
		raw      string
		addr     string
		password string
	}{
		{"redis:6379", "redis:6379", ""},
		{"127.0.0.1:6379", "127.0.0.1:6379", ""},
		{"redis://:hunter2-long-enough@redis:6379/0", "redis:6379", "hunter2-long-enough"},
		{"rediss://:hunter2-long-enough@redis:6379", "redis:6379", "hunter2-long-enough"},
		{"redis://redis:6379", "redis:6379", ""},
		{"  redis:6379  ", "redis:6379", ""},
	}
	for _, testCase := range cases {
		addr, password := ParseRedisAddress(testCase.raw)
		if addr != testCase.addr || password != testCase.password {
			t.Fatalf("ParseRedisAddress(%q) = %q, %q; want %q, %q",
				testCase.raw, addr, password, testCase.addr, testCase.password)
		}
	}
}

// The lease has to exceed the worst case for one detection, and the derivation
// is what makes the number defensible: the policy timeout is capped at 90s, and
// a BROWSER check spends that budget twice. This is the guarantee that replaces
// "the slowest detection is probably well under the lease".
func TestValidateProductionJobLease(t *testing.T) {
	cases := []struct {
		value   string
		wantErr bool
	}{
		{"", false},    // unset keeps the store default
		{"270", true},  // exactly the floor is not enough
		{"269", true},  // below it
		{"271", false}, // just above
		{"600", false},
		{"0", true},
		{"-60", true},
		{"ten", true},
	}
	for _, testCase := range cases {
		t.Run("value="+testCase.value, func(t *testing.T) {
			values := productionEnvWithSecrets()
			values["ALERT_SECRET_KEY"] = keyMaterial(1)
			if testCase.value != "" {
				values["JOB_LEASE_SECONDS"] = testCase.value
			}
			err := ValidateProductionAPI(func(name string) string { return values[name] })
			if testCase.wantErr && err == nil {
				t.Fatal("an unsafe job lease was accepted")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if testCase.wantErr && !strings.Contains(err.Error(), "JOB_LEASE_SECONDS") {
				t.Fatalf("error does not name the variable: %v", err)
			}
		})
	}
}

// The compose topology publishes the API port to the host, so an ungated
// /metrics endpoint is an information disclosure in production. Development
// keeps the empty-token default, which is why the check lives in the
// production validators only.
func TestValidateProductionRequiresMetricsToken(t *testing.T) {
	values := productionEnvWithSecrets()
	delete(values, "METRICS_TOKEN")
	err := ValidateProductionAPI(func(name string) string { return values[name] })
	if err == nil {
		t.Fatal("expected an open /metrics endpoint to be rejected in production")
	}
	if !strings.Contains(err.Error(), "METRICS_TOKEN") {
		t.Fatalf("error %q does not name METRICS_TOKEN", err)
	}

	values["METRICS_TOKEN"] = "short"
	if err := ValidateProductionAPI(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected a short METRICS_TOKEN to be rejected in production")
	}
}
