package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func PositiveInt(get func(string) string, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(get(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

const (
	EnvironmentProduction        = "production"
	defaultAdminPassword         = "change-me-before-production"
	defaultDatabasePass          = "webmonitoring"
	defaultObjectSecret          = "replace-with-a-random-secret"
	defaultWorkerToken           = "replace-with-a-random-token"
	DefaultEvidenceRetentionDays = 90
	DefaultAuditRetentionDays    = 365
)

func IsProduction(get func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(get("APP_ENV")), EnvironmentProduction)
}

func ValidateProductionAPI(get func(string) string) error {
	if !IsProduction(get) {
		return nil
	}
	var failures []error
	failures = append(failures, validateRequired(get,
		"POSTGRES_DSN", "REDIS_ADDR", "S3_ENDPOINT", "S3_ACCESS_KEY",
		"S3_SECRET_KEY", "BROWSER_WORKER_URL", "BROWSER_WORKER_TOKEN",
		"ALERT_SECRET_KEY", "ADMIN_PASSWORD",
	)...)
	if get("ADMIN_PASSWORD") == defaultAdminPassword {
		failures = append(failures, errors.New("ADMIN_PASSWORD must be changed in production"))
	}
	// ADMIN_PASSWORD is checked here rather than in validateCommon, which the
	// worker also calls: the worker never reads it, and the compose worker
	// service does not set it, so validating it there stopped the worker tier
	// from starting in production.
	failures = append(failures, validateMinLength(get, "ADMIN_PASSWORD", 12)...)
	failures = append(failures, validateCommon(get)...)
	failures = append(failures, validateDatabaseDSN(get("POSTGRES_DSN"))...)
	return errors.Join(failures...)
}

func ValidateProductionWorker(get func(string) string) error {
	if !IsProduction(get) {
		return nil
	}
	var failures []error
	// WORKER_ID is deliberately not required. A value set once in the
	// environment is shared by every replica under `--scale`, which is the
	// collision that config.WorkerIdentity avoids by generating a per-process
	// name; requiring it would push deployments back onto a fixed consumer.
	failures = append(failures, validateRequired(get,
		"POSTGRES_DSN", "REDIS_ADDR", "S3_ENDPOINT", "S3_ACCESS_KEY",
		"S3_SECRET_KEY", "ALERT_SECRET_KEY",
		"BROWSER_WORKER_URL", "BROWSER_WORKER_TOKEN",
	)...)
	if workerID := strings.TrimSpace(get("WORKER_ID")); legacyWorkerIDs[workerID] {
		failures = append(failures, fmt.Errorf(
			"WORKER_ID must not be %q: it is a fixed name shared by every replica, so scaled workers would reclaim each other's in-flight jobs; unset it to generate a per-process identity", workerID))
	}
	failures = append(failures, validateCommon(get)...)
	failures = append(failures, validateDatabaseDSN(get("POSTGRES_DSN"))...)
	return errors.Join(failures...)
}

func validateCommon(get func(string) string) []error {
	var failures []error
	if get("S3_ACCESS_KEY") == "webmonitoring" {
		failures = append(failures, errors.New("S3_ACCESS_KEY must be changed in production"))
	}
	if get("S3_SECRET_KEY") == defaultObjectSecret {
		failures = append(failures, errors.New("S3_SECRET_KEY must be changed in production"))
	}
	if get("BROWSER_WORKER_TOKEN") == defaultWorkerToken {
		failures = append(failures, errors.New("BROWSER_WORKER_TOKEN must be changed in production"))
	}
	if !strings.EqualFold(strings.TrimSpace(get("SESSION_SECURE")), "true") {
		failures = append(failures, errors.New("SESSION_SECURE must be true in production"))
	}
	if strings.EqualFold(strings.TrimSpace(get("ALLOW_PRIVATE_TARGETS")), "true") {
		failures = append(failures, errors.New("ALLOW_PRIVATE_TARGETS must be false in production"))
	}
	failures = append(failures, validateWorkerEndpoint(get)...)
	failures = append(failures, validateMinLength(get, "S3_ACCESS_KEY", 8)...)
	failures = append(failures, validateMinLength(get, "S3_SECRET_KEY", 16)...)
	failures = append(failures, validateMinLength(get, "BROWSER_WORKER_TOKEN", 16)...)
	key, err := base64.StdEncoding.DecodeString(get("ALERT_SECRET_KEY"))
	if err != nil || len(key) != 32 {
		failures = append(failures, errors.New("ALERT_SECRET_KEY must be a base64-encoded 32-byte key"))
	}
	failures = append(failures, validateSMTP(get)...)
	return failures
}

// validateWorkerEndpoint enforces the transport constraint ADR-0004 depends on.
// Browser-mode login-state detection sends decrypted customer credentials to
// this endpoint, so it has to be TLS, or a target that cannot be a public
// hostname: a private or loopback address, or a bare service name that the
// orchestrator's internal DNS resolves.
//
// This check is lexical on purpose. It cannot resolve names, so a dotted host
// over plain http is rejected rather than assumed to be internal -- that is the
// case where a mistake would put credentials on a public network.
func validateWorkerEndpoint(get func(string) string) []error {
	raw := strings.TrimSpace(get("BROWSER_WORKER_URL"))
	if raw == "" {
		// Absence is already reported by validateRequired where it matters.
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return []error{errors.New("BROWSER_WORKER_URL must be a valid URL")}
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
	default:
		return []error{errors.New("BROWSER_WORKER_URL must use http or https")}
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return nil
		}
		return []error{errors.New("BROWSER_WORKER_URL must use https when it does not point at a private address")}
	}
	if !strings.Contains(host, ".") {
		return nil
	}
	return []error{errors.New("BROWSER_WORKER_URL must use https for a domain name, because decrypted login credentials are sent to it")}
}

// validateSMTP enforces a complete SMTP configuration only when email
// notifications are enabled (SMTP_HOST set). Email delivery is optional, so an
// absent SMTP_HOST is not an error.
func validateSMTP(get func(string) string) []error {
	if strings.TrimSpace(get("SMTP_HOST")) == "" {
		return nil
	}
	var failures []error
	failures = append(failures, validateRequired(get, "SMTP_HOST", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM")...)
	if port := strings.TrimSpace(get("SMTP_PORT")); port != "" {
		if parsed, err := strconv.Atoi(port); err != nil || parsed < 1 || parsed > 65535 {
			failures = append(failures, errors.New("SMTP_PORT must be between 1 and 65535"))
		}
	}
	return failures
}

func validateMinLength(get func(string) string, name string, minimum int) []error {
	if len([]byte(strings.TrimSpace(get(name)))) < minimum {
		return []error{fmt.Errorf("%s must be at least %d bytes in production", name, minimum)}
	}
	return nil
}

func validateRequired(get func(string) string, names ...string) []error {
	var failures []error
	for _, name := range names {
		if strings.TrimSpace(get(name)) == "" {
			failures = append(failures, fmt.Errorf("%s is required in production", name))
		}
	}
	return failures
}

func validateDatabaseDSN(raw string) []error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return []error{errors.New("POSTGRES_DSN must contain a valid database host")}
	}
	if password, ok := parsed.User.Password(); !ok || password == "" {
		return []error{errors.New("POSTGRES_DSN must contain database credentials")}
	} else if password == defaultDatabasePass {
		return []error{errors.New("POSTGRES_DSN must not use the default database password")}
	} else if len([]byte(password)) < 16 {
		return []error{errors.New("POSTGRES_DSN password must be at least 16 bytes in production")}
	}
	return nil
}
