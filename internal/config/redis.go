package config

import (
	"net/url"
	"strings"
)

// ParseRedisAddress splits REDIS_ADDR into the host:port and password the Redis
// client needs. Two forms are accepted:
//
//	redis:6379                        -- no password
//	redis://:password@redis:6379/0    -- password taken from the URL
//	rediss://:password@redis:6379/0   -- same, for a TLS-terminated proxy
//
// Parsing here rather than at each call site keeps every client -- the two job
// queues, the session store and the login limiter -- on the same rules, and it
// leaves the function signatures alone: a caller that passes a bare host:port
// (which is what the tests pass) gets its input back unchanged and no password.
//
// A malformed URL is not an error. It falls through to the bare-address branch
// so an address that merely contains a colon is still usable, and a genuinely
// unusable value fails at connect time with a message from the client.
func ParseRedisAddress(raw string) (addr, password string) {
	trimmed := strings.TrimSpace(raw)
	scheme, _, found := strings.Cut(trimmed, "://")
	if !found || (scheme != "redis" && scheme != "rediss") {
		return trimmed, ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return trimmed, ""
	}
	if parsed.User != nil {
		password, _ = parsed.User.Password()
	}
	return parsed.Host, password
}
