package store

import "testing"

func poolEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestPoolConfigDefaultsBoundThePool(t *testing.T) {
	config := PoolConfigFromEnv(poolEnv(nil))
	if config != DefaultPoolConfig() {
		t.Fatalf("an unset environment changed the defaults: %#v", config)
	}
	// The default has to be a real bound. An unbounded pool is the failure this
	// configuration exists to prevent, so "no limit" must not be the default.
	if config.MaxConns <= 0 {
		t.Fatalf("default MaxConns must be a positive bound, got %d", config.MaxConns)
	}
	if int(config.MaxConns) >= 100 {
		t.Fatalf("default MaxConns must leave room for other replicas under the server's default max_connections of 100, got %d", config.MaxConns)
	}
}

func TestPoolConfigReadsOverrides(t *testing.T) {
	config := PoolConfigFromEnv(poolEnv(map[string]string{
		"POSTGRES_MAX_CONNS":                 "25",
		"POSTGRES_MIN_CONNS":                 "3",
		"POSTGRES_MAX_CONN_LIFETIME_SECONDS": "120",
		"POSTGRES_MAX_CONN_IDLE_SECONDS":     "60",
		"POSTGRES_HEALTH_CHECK_SECONDS":      "30",
		"POSTGRES_CONNECT_TIMEOUT_SECONDS":   "7",
	}))
	defaults := DefaultPoolConfig()
	if config.MaxConns != 25 || config.MinConns != 3 {
		t.Fatalf("connection counts not read: %#v", config)
	}
	if config.MaxConnLifetime != 120e9 || config.MaxConnIdleTime != 60e9 ||
		config.HealthCheckPeriod != 30e9 || config.ConnectTimeout != 7e9 {
		t.Fatalf("durations not read: %#v", config)
	}
	if config.MaxConns == defaults.MaxConns {
		t.Fatal("override did not take effect")
	}
}

// Values the parser cannot use must leave the default in place rather than
// widening the pool. The production gate is what refuses them; this only has to
// ensure a bad value is never the reason a limit disappears.
func TestPoolConfigKeepsDefaultsForUnusableValues(t *testing.T) {
	config := PoolConfigFromEnv(poolEnv(map[string]string{
		"POSTGRES_MAX_CONNS":                 "many",
		"POSTGRES_MIN_CONNS":                 "-4",
		"POSTGRES_MAX_CONN_LIFETIME_SECONDS": "0",
		"POSTGRES_CONNECT_TIMEOUT_SECONDS":   "soon",
	}))
	if config != DefaultPoolConfig() {
		t.Fatalf("unusable values changed the configuration: %#v", config)
	}
}

// A value past the field's range must not wrap into a negative limit, which
// pgxpool reads as "use your own default" -- the opposite of what was asked.
func TestPoolConfigIgnoresValuesBeyondInt32(t *testing.T) {
	config := PoolConfigFromEnv(poolEnv(map[string]string{"POSTGRES_MAX_CONNS": "99999999999999"}))
	if config.MaxConns <= 0 {
		t.Fatalf("out-of-range value produced a non-positive limit: %d", config.MaxConns)
	}
	if config.MaxConns != DefaultPoolConfig().MaxConns {
		t.Fatalf("out-of-range value was applied instead of the default: %d", config.MaxConns)
	}
}
