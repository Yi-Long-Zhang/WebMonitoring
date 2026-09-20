package store

import (
	"encoding/base64"
	"testing"

	"webmonitoring/internal/security"
)

func channelKey(seed string) string {
	raw := []byte(seed)
	for len(raw) < 32 {
		raw = append(raw, 'x')
	}
	return base64.StdEncoding.EncodeToString(raw[:32])
}

// The compatibility fallback exists for rows written before encryption was
// introduced. It must not also cover a value that was encrypted and then failed
// to decrypt: that is a wrong-key condition, and returning the ciphertext would
// hand the dispatcher a bogus URL and, worse, sign webhooks with a bogus HMAC
// secret while nothing reported an error.
func TestDecryptChannelFieldToleratesOnlyUnversionedPlaintext(t *testing.T) {
	mine, err := security.NewKeyring("v1:" + channelKey("first-key-material"))
	if err != nil {
		t.Fatal(err)
	}

	// A pre-encryption row is plaintext with no version prefix.
	plaintext := "https://hooks.example.com/services/abc"
	if got, err := decryptChannelField(mine, plaintext, "tenant:channel:url"); err != nil || got != plaintext {
		t.Fatalf("plaintext fallback returned %q, %v", got, err)
	}
	if got, err := decryptChannelField(mine, "", "tenant:channel:url"); err != nil || got != "" {
		t.Fatalf("empty value returned %q, %v", got, err)
	}

	// A value sealed by a different key at the same version, which is exactly
	// what a botched rotation produces.
	other, err := security.NewKeyring("v1:" + channelKey("second-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Encrypt("secret", "tenant:channel:secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptChannelField(mine, foreign, "tenant:channel:secret"); err == nil {
		t.Fatal("a versioned credential that failed to decrypt was passed through")
	}

	// A version the keyring does not hold is refused for the same reason.
	if _, err := decryptChannelField(mine, "v9:"+base64.RawURLEncoding.EncodeToString([]byte("nope")), "ctx"); err == nil {
		t.Fatal("an unconfigured key version was passed through")
	}
}
