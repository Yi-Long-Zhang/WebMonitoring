package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxPostgresConns is the ceiling a single replica may claim. The gate needs a
// ceiling as much as a floor: a bound so large it can never be reached is not a
// bound, and a deployment that set one would have protected nothing while
// believing otherwise.
const maxPostgresConns = 1000

// validatePostgresPool refuses a production deployment whose connection pool is
// effectively unbounded or internally inconsistent.
//
// The pool is the only thing stopping one replica from consuming the server's
// whole max_connections, so zero -- which pgxpool reads as "use your own
// default" rather than "no limit" -- is refused instead of passed through, the
// same way the rate limiter refuses a zero allowance. A MIN_CONNS above
// MAX_CONNS is refused because pgxpool clamps it silently, leaving the operator
// with a number that was never applied.
func validatePostgresPool(get func(string) string) []error {
	var failures []error
	maxConns, maxPresent, err := parsePoolInt(get, "POSTGRES_MAX_CONNS")
	switch {
	case err != nil:
		failures = append(failures, errors.New("POSTGRES_MAX_CONNS must be an integer"))
	case maxPresent && maxConns <= 0:
		failures = append(failures, errors.New("POSTGRES_MAX_CONNS must be positive in production: 0 leaves the pool on its own default instead of the limit you asked for"))
	case maxPresent && maxConns > maxPostgresConns:
		failures = append(failures, fmt.Errorf("POSTGRES_MAX_CONNS must not exceed %d: a bound that large is not a bound, and every replica would claim it", maxPostgresConns))
	}
	minConns, minPresent, err := parsePoolInt(get, "POSTGRES_MIN_CONNS")
	switch {
	case err != nil:
		failures = append(failures, errors.New("POSTGRES_MIN_CONNS must be an integer"))
	case minPresent && minConns < 0:
		failures = append(failures, errors.New("POSTGRES_MIN_CONNS must not be negative"))
	case minPresent && maxPresent && maxConns > 0 && minConns > maxConns:
		failures = append(failures, fmt.Errorf("POSTGRES_MIN_CONNS (%d) must not exceed POSTGRES_MAX_CONNS (%d)", minConns, maxConns))
	}
	for _, name := range []string{
		"POSTGRES_MAX_CONN_LIFETIME_SECONDS",
		"POSTGRES_MAX_CONN_IDLE_SECONDS",
		"POSTGRES_HEALTH_CHECK_SECONDS",
		"POSTGRES_CONNECT_TIMEOUT_SECONDS",
	} {
		raw := strings.TrimSpace(get(name))
		if raw == "" {
			continue
		}
		if value, convErr := strconv.Atoi(raw); convErr != nil || value <= 0 {
			failures = append(failures, fmt.Errorf("%s must be a positive number of seconds", name))
		}
	}
	return failures
}

func parsePoolInt(get func(string) string, name string) (int, bool, error) {
	raw := strings.TrimSpace(get(name))
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, err
	}
	return value, true, nil
}
