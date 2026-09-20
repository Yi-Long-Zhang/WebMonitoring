package config

import "strings"

// SecretKeyMaterial returns the configured key material for the credential
// cipher. ALERT_SECRET_KEYS (plural) is the versioned form and takes
// precedence; ALERT_SECRET_KEY (singular) is the original single-key form.
//
// Both are read here rather than at each call site because the API and the
// worker have to agree: a worker that read only the singular variable would
// fail to decrypt what the API wrote, and that mismatch only shows up at
// runtime, against real stored credentials.
func SecretKeyMaterial(get func(string) string) string {
	if keys := strings.TrimSpace(get("ALERT_SECRET_KEYS")); keys != "" {
		return keys
	}
	return get("ALERT_SECRET_KEY")
}
