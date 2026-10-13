package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func PositiveInt(get func(string) string, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(get(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// NonNegativeInt is PositiveInt for a knob where 0 is a value the operator has
// to be able to express rather than a typo to be corrected: it returns the
// parsed 0 instead of the fallback, and still substitutes the fallback for
// unset, empty, unparseable or negative input.
//
// The distinction matters because PositiveInt cannot represent "off" at all -- a
// caller that guards on the result being > 0 is guarding on a condition the
// parser has already made impossible, so the off-branch is dead code and the
// documented way to disable the knob silently does the opposite.
func NonNegativeInt(get func(string) string, name string, fallback int) int {
	raw := strings.TrimSpace(get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

const (
	EnvironmentProduction        = "production"
	defaultDatabasePass          = "webmonitoring"
	defaultObjectSecret          = "replace-with-a-random-secret"
	defaultRedisPassword         = "webmonitoring"
	defaultWorkerToken           = "replace-with-a-random-token"
	DefaultEvidenceRetentionDays = 90
	DefaultAuditRetentionDays    = 365
	// DefaultJobRetentionDays bounds detection_jobs: at the fastest supported
	// interval one asset produces ~288 rows a day, so an unbounded table
	// eventually dwarfs every other table in the installation. Terminal rows
	// only -- the sweep never touches QUEUED or RUNNING state.
	DefaultJobRetentionDays = 30
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
		"ADMIN_PASSWORD",
	)...)
	// The published default lives in deployments/compose/.env.default, not in
	// this package: the point of the check is to reject a deployment that kept
	// the development password, and embedding it here would put it back in the
	// source. Skipped when no default file is present -- validateRequired above
	// already guarantees ADMIN_PASSWORD was supplied at all.
	if fallback := DefaultAdminPassword(get); fallback != "" && get("ADMIN_PASSWORD") == fallback {
		failures = append(failures, errors.New("ADMIN_PASSWORD must not be the published development default"))
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
		"S3_SECRET_KEY",
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
	failures = append(failures, validateSecretKeys(get)...)
	failures = append(failures, validateRedisCredentials(get)...)
	failures = append(failures, validateJobLease(get)...)
	failures = append(failures, validatePostgresPool(get)...)
	failures = append(failures, validateRateLimits(get)...)
	failures = append(failures, validateAlertGate(get)...)
	failures = append(failures, validateSMTP(get)...)
	failures = append(failures, validateAllowedOrigins(get)...)
	failures = append(failures, validateMetricsToken(get)...)
	return failures
}

// validateMetricsToken refuses a production deployment that leaves the
// /metrics endpoint open. The compose topology publishes the API port to the
// host, so without a token anyone who can reach the host can read process,
// pool and incident internals. An empty token stays legal in development,
// where scraping without ceremony matters more than the exposure.
func validateMetricsToken(get func(string) string) []error {
	if strings.TrimSpace(get("METRICS_TOKEN")) == "" {
		return []error{errors.New("METRICS_TOKEN is required in production: it gates the /metrics endpoint, and the compose topology publishes the API port to the host")}
	}
	return validateMinLength(get, "METRICS_TOKEN", 16)
}

// validateAllowedOrigins rejects a wildcard ALLOWED_ORIGINS in production. The
// parser silently drops "*", so without this check the operator would believe
// they had opened the API to every origin while the API stayed same-origin --
// and the failure would show up as an opaque 403 in a browser console. An https
// origin is required for the same reason the session cookie is Secure: a
// cross-origin console over plain http would send credentials in the clear.
func validateAllowedOrigins(get func(string) string) []error {
	raw := strings.TrimSpace(get("ALLOWED_ORIGINS"))
	if raw == "" {
		return nil
	}
	var failures []error
	for _, entry := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(entry)
		if origin == "" {
			continue
		}
		if origin == "*" {
			failures = append(failures, errors.New("ALLOWED_ORIGINS must list explicit origins; a wildcard is refused because credentialed requests cannot use one"))
			continue
		}
		if !strings.HasPrefix(origin, "https://") {
			failures = append(failures, fmt.Errorf("ALLOWED_ORIGINS entry %q must use https", origin))
		}
	}
	return failures
}

// validateSecretKeys checks the credential-cipher key material. Either variable
// is accepted -- ALERT_SECRET_KEYS (versioned, comma separated) takes precedence
// over ALERT_SECRET_KEY -- but at least one must be present, and every entry
// must be a base64-encoded 32-byte key under a unique version label. A
// deployment that migrated to the plural form is not rejected for having
// dropped the singular one, and a duplicate version is refused because it would
// make which key sealed a ciphertext ambiguous.
func validateSecretKeys(get func(string) string) []error {
	// The plural variable is an explicit list, so every entry in it must be
	// labelled. An unlabelled entry there would silently become v1, and a silent
	// version assignment is a silent decryption failure after a rotation. The
	// singular variable is the original single-key form and stays unlabelled.
	explicitList := strings.TrimSpace(get("ALERT_SECRET_KEYS")) != ""
	material := SecretKeyMaterial(get)
	if strings.TrimSpace(material) == "" {
		return []error{errors.New("ALERT_SECRET_KEY or ALERT_SECRET_KEYS must be set in production")}
	}
	var failures []error
	seen := map[int]bool{}
	for _, entry := range strings.Split(material, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		version, key := 1, entry
		if label, rest, found := strings.Cut(entry, ":"); found {
			parsed, err := strconv.Atoi(strings.TrimPrefix(label, "v"))
			if !strings.HasPrefix(label, "v") || err != nil || parsed < 1 {
				failures = append(failures, fmt.Errorf("ALERT_SECRET_KEYS entry %q must carry a version label such as v1 or v2", entry))
				continue
			}
			version, key = parsed, rest
		} else if explicitList {
			failures = append(failures, fmt.Errorf("ALERT_SECRET_KEYS entry %q must carry a version label such as v1 or v2", entry))
			continue
		}
		if seen[version] {
			failures = append(failures, fmt.Errorf("ALERT_SECRET_KEYS configures version v%d more than once", version))
			continue
		}
		seen[version] = true
		decoded, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(key)
		}
		if err != nil || len(decoded) != 32 {
			failures = append(failures, fmt.Errorf("ALERT_SECRET_KEYS entry v%d must be a base64-encoded 32-byte key", version))
		}
	}
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
	// Refusing sslmode=disable is what actually enforces transport encryption;
	// documenting it does not. Compose sets POSTGRES_SSLMODE for exactly this,
	// and the TLS overlay turns it to verify-full.
	if mode := parsed.Query().Get("sslmode"); mode == "" || mode == "disable" {
		return []error{errors.New("POSTGRES_DSN must set sslmode other than disable in production (POSTGRES_SSLMODE in the compose environment)")}
	}
	return nil
}

// minJobLease is the shortest lease that can be provably safe, and the
// derivation matters more than the number.
//
// One detection is bounded by the policy's TimeoutSeconds, which the API
// refuses to accept above 90s. A BROWSER check spends that budget twice: once
// loading the sign-in page and once navigating to the target. So the worst case
// for a single job is three times the policy cap. A lease at or below that lets
// a peer replica reap a job that is still working, and ReapTimedOut does not
// check which replica owns a job -- this inequality is the entire guarantee.
const minJobLease = 3 * 90 * time.Second

// validateJobLease enforces that inequality instead of leaving it to a comment.
// The alternative, a heartbeat column the reaper consults, would be correct by
// construction but needs a migration, a repository method and worker plumbing;
// it is the follow-up if a policy timeout ever becomes unbounded.
func validateJobLease(get func(string) string) []error {
	raw := strings.TrimSpace(get("JOB_LEASE_SECONDS"))
	if raw == "" {
		return nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return []error{errors.New("JOB_LEASE_SECONDS must be an integer number of seconds")}
	}
	if time.Duration(seconds)*time.Second <= minJobLease {
		return []error{fmt.Errorf(
			"JOB_LEASE_SECONDS must be greater than %d: one detection is bounded by the policy timeout (max 90s) and a BROWSER check spends that budget twice, so a shorter lease lets a peer replica mark a job TIMED_OUT while it is still running",
			int(minJobLease.Seconds()))}
	}
	return nil
}

// validateRedisCredentials requires a real password when REDIS_ADDR carries
// credentials. Demanding it only in the credentialed form leaves the bare
// host:port form -- used by the integration tests and a single-node dev stack --
// working unchanged, so nothing has to change until a deployment opts in.
func validateRedisCredentials(get func(string) string) []error {
	_, password := ParseRedisAddress(get("REDIS_ADDR"))
	switch {
	case password == "":
		return nil
	case password == defaultRedisPassword:
		return []error{errors.New("REDIS_ADDR password must not be the default in production")}
	case len(password) < 16:
		return []error{errors.New("REDIS_ADDR password must be at least 16 characters in production")}
	}
	return nil
}

// validateRateLimits refuses a production deployment whose request allowance is
// disabled or absurdly loose.
//
// Zero is the value that turns a class off, so it is read as "unlimited" rather
// than as "the default" -- and a production installation that meant to configure
// a limit and typed 0 would otherwise end up with none while believing it had
// set one. The ceiling is generous but finite: a limit high enough to be
// indistinguishable from unlimited protects nothing, and the whole point of the
// control is that one tenant cannot consume the installation.
func validateRateLimits(get func(string) string) []error {
	var failures []error
	checks := []struct {
		name    string
		ceiling int
	}{
		{"RATE_LIMIT_READ_PER_WINDOW", 100000},
		{"RATE_LIMIT_WRITE_PER_WINDOW", 20000},
		{"RATE_LIMIT_BULK_PER_WINDOW", 2000},
	}
	for _, check := range checks {
		raw := strings.TrimSpace(get(check.name))
		if raw == "" {
			// Unset means the built-in default, which is a real limit.
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s must be an integer", check.name))
			continue
		}
		if value <= 0 {
			failures = append(failures, fmt.Errorf("%s must be positive in production: 0 disables the limit for that class of request", check.name))
			continue
		}
		if value > check.ceiling {
			failures = append(failures, fmt.Errorf("%s must not exceed %d, which is effectively no limit", check.name, check.ceiling))
		}
	}
	return failures
}

// validateAlertGate refuses a production deployment that has disabled the
// outbound notification controls by setting one of them to zero.
//
// Zero turns a control off, which is a legitimate thing to want in development
// but not in an installation that sends alerts to real people: a channel that
// has been dead for a day and one that is being handed a thousand messages an
// hour are both silent failures, and the operator would be setting the value
// believing it configured something. Unlike the request limiter there is no
// ceiling here -- a busy installation may legitimately want a large allowance --
// so only the disabling value is refused.
func validateAlertGate(get func(string) string) []error {
	var failures []error
	for _, name := range []string{
		"ALERT_MAX_PER_CHANNEL_PER_WINDOW",
		"ALERT_FAILURE_THRESHOLD",
	} {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s must be an integer", name))
			continue
		}
		if value <= 0 {
			failures = append(failures, fmt.Errorf("%s must be positive in production: 0 disables that notification control", name))
		}
	}
	if raw := strings.TrimSpace(get("ALERT_GATE_WINDOW_SECONDS")); raw != "" {
		if value, err := strconv.Atoi(raw); err != nil || value <= 0 {
			failures = append(failures, errors.New("ALERT_GATE_WINDOW_SECONDS must be a positive number of seconds"))
		}
	}
	return failures
}
