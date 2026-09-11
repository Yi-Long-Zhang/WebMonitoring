package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSecretCipherRoundTripAndContextBinding(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secrets, err := NewSecretCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := secrets.Encrypt("sensitive-value", "tenant:channel:url")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "sensitive-value") {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := secrets.Decrypt(encrypted, "tenant:channel:url")
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "sensitive-value" {
		t.Fatalf("unexpected plaintext: %q", decrypted)
	}
	if _, err := secrets.Decrypt(encrypted, "other-tenant:channel:url"); err == nil {
		t.Fatal("ciphertext decrypted with different associated data")
	}
}

func TestSecretCipherRejectsInvalidKey(t *testing.T) {
	if _, err := NewSecretCipher(base64.StdEncoding.EncodeToString([]byte("too-short"))); err == nil {
		t.Fatal("expected invalid key error")
	}
}
