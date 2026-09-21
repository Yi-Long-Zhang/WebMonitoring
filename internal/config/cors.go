package config

import "strings"

// AllowedOrigins parses ALLOWED_ORIGINS into the set of origins the API will
// answer cross-origin requests from. An empty value means same-origin only:
// the browser is not told it may read the response, which is the correct
// default for the single-host Compose topology where the console and the API
// are served by the same nginx.
//
// A value of "*" is deliberately not honoured. Every authenticated endpoint
// relies on a session cookie, and the browser refuses to send credentials to a
// wildcard origin -- so "*" here would either be ignored or, worse, be read as
// "authentication optional". An operator who wants the console on a separate
// host lists that host.
func AllowedOrigins(get func(string) string) map[string]struct{} {
	raw := strings.TrimSpace(get("ALLOWED_ORIGINS"))
	if raw == "" {
		return nil
	}
	origins := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(entry)
		if origin == "" || origin == "*" {
			continue
		}
		// Trailing slashes are stripped because the Origin header a browser
		// sends never carries one, and a configured "https://host/" would
		// otherwise never match.
		origins[strings.TrimRight(origin, "/")] = struct{}{}
	}
	if len(origins) == 0 {
		return nil
	}
	return origins
}
