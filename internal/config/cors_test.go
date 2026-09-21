package config

import "testing"

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// TestAllowedOriginsIsEmptyWithoutConfiguration pins the same-origin default.
// A nil map means the middleware never echoes an Origin, which is what the
// single-host topology wants.
func TestAllowedOriginsIsEmptyWithoutConfiguration(t *testing.T) {
	for name, value := range map[string]string{
		"unset":  "",
		"spaces": "   ",
		"commas": " , , ",
	} {
		if got := AllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": value})); got != nil {
			t.Fatalf("%s: expected no origins, got %v", name, got)
		}
	}
}

// TestAllowedOriginsDropsWildcard pins that "*" is refused rather than honoured.
// Credentialed requests cannot use a wildcard origin, so accepting it would
// either be ignored by the browser or read as "authentication optional" -- both
// worse than the same-origin default. Production validation rejects it outright;
// the parser has to agree so the two cannot drift.
func TestAllowedOriginsDropsWildcard(t *testing.T) {
	if got := AllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": "*"})); got != nil {
		t.Fatalf("wildcard was accepted: %v", got)
	}
	mixed := AllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": "*, https://console.example"}))
	if _, ok := mixed["*"]; ok {
		t.Fatal("wildcard survived alongside a real origin")
	}
	if _, ok := mixed["https://console.example"]; !ok {
		t.Fatalf("the real origin was dropped with the wildcard: %v", mixed)
	}
}

// TestAllowedOriginsNormalizesTrailingSlash pins the normalization: the Origin
// header a browser sends never carries a trailing slash, so a configured
// "https://host/" would never match without this.
func TestAllowedOriginsNormalizesTrailingSlash(t *testing.T) {
	origins := AllowedOrigins(envFrom(map[string]string{
		"ALLOWED_ORIGINS": "https://console.example/ , http://other.example///",
	}))
	if _, ok := origins["https://console.example"]; !ok {
		t.Fatalf("trailing slash was not stripped: %v", origins)
	}
	if _, ok := origins["http://other.example"]; !ok {
		t.Fatalf("repeated slashes were not stripped: %v", origins)
	}
}

// TestValidateAllowedOriginsRejectsWildcardAndPlainHTTP pins the production
// gate. The parser drops "*" silently, so without this check an operator would
// believe they had opened the API to every origin while it stayed same-origin,
// and the only symptom would be an opaque 403 in a browser console.
func TestValidateAllowedOriginsRejectsWildcardAndPlainHTTP(t *testing.T) {
	if failures := validateAllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": ""})); len(failures) != 0 {
		t.Fatalf("empty allowlist should pass: %v", failures)
	}
	if failures := validateAllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": "https://console.example"})); len(failures) != 0 {
		t.Fatalf("an https origin should pass: %v", failures)
	}
	if failures := validateAllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": "*"})); len(failures) == 0 {
		t.Fatal("wildcard was accepted in production")
	}
	if failures := validateAllowedOrigins(envFrom(map[string]string{"ALLOWED_ORIGINS": "http://console.example"})); len(failures) == 0 {
		t.Fatal("plaintext origin was accepted in production")
	}
}
