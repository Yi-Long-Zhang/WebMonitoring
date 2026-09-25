package store

import (
	"strconv"
	"strings"
	"time"
)

// PoolConfig bounds the connection pool this process opens against PostgreSQL.
//
// The pool is what stops one replica from consuming the server's entire
// max_connections, and what makes a stalled query cost a single slot instead of
// the process. Every field has a default, and a zero value means "keep the
// default" rather than "unbounded", so an unset variable can never produce a
// limit-free pool.
type PoolConfig struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration
}

// DefaultPoolConfig is what a deployment that configures nothing gets. MaxConns
// is deliberately far below the server's default max_connections of 100: one
// installation runs several API and worker replicas against one server, so the
// budget has to be divided among them rather than claimed by each.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxConns:          10,
		MinConns:          1,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   15 * time.Minute,
		HealthCheckPeriod: time.Minute,
		ConnectTimeout:    10 * time.Second,
	}
}

// PoolConfigFromEnv reads the pool bounds, keeping the default for any variable
// that is unset or unparseable. Ignoring a value here is safe because the
// production gate (internal/config) is what refuses a deployment that set a
// bound this parser would silently discard -- the fallback is never the only
// thing standing between an operator and an unbounded pool.
func PoolConfigFromEnv(get func(string) string) PoolConfig {
	config := DefaultPoolConfig()
	if value, ok := envInt32(get, "POSTGRES_MAX_CONNS"); ok && value > 0 {
		config.MaxConns = value
	}
	if value, ok := envInt32(get, "POSTGRES_MIN_CONNS"); ok && value >= 0 {
		config.MinConns = value
	}
	config.MaxConnLifetime = envSeconds(get, "POSTGRES_MAX_CONN_LIFETIME_SECONDS", config.MaxConnLifetime)
	config.MaxConnIdleTime = envSeconds(get, "POSTGRES_MAX_CONN_IDLE_SECONDS", config.MaxConnIdleTime)
	config.HealthCheckPeriod = envSeconds(get, "POSTGRES_HEALTH_CHECK_SECONDS", config.HealthCheckPeriod)
	config.ConnectTimeout = envSeconds(get, "POSTGRES_CONNECT_TIMEOUT_SECONDS", config.ConnectTimeout)
	return config
}

// envInt32 parses into the range the pool itself uses, so a value far outside it
// cannot wrap into a negative limit that pgxpool would read as "use the default".
func envInt32(get func(string) string, name string) (int32, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(get(name)), 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(value), true
}

func envSeconds(get func(string) string, name string, fallback time.Duration) time.Duration {
	seconds, ok := envInt32(get, name)
	if !ok || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
