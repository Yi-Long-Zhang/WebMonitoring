package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func signedURLTestKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSignedURLSignerRoundTrip(t *testing.T) {
	signer, err := NewSignedURLSigner(signedURLTestKey())
	if err != nil {
		t.Fatal(err)
	}
	message := "tenant|evidence|1234567890"
	token := signer.Encode(message)
	if strings.ContainsAny(token, "/+") {
		t.Fatalf("token must be URL-safe, got %q", token)
	}
	decoded, ok := signer.Decode(token)
	if !ok || decoded != message {
		t.Fatalf("round trip mismatch: ok=%v message=%q", ok, decoded)
	}
}

func TestSignedURLSignerRejectsTamperAndForeignKey(t *testing.T) {
	signer, err := NewSignedURLSigner(signedURLTestKey())
	if err != nil {
		t.Fatal(err)
	}
	token := signer.Encode("payload")

	if _, ok := signer.Decode("garbage-without-dot"); ok {
		t.Fatal("expected malformed token to be rejected")
	}
	// Flip a character in the signature.
	flipped := token[:len(token)-1] + "0"
	if _, ok := signer.Decode(flipped); ok {
		t.Fatal("expected tampered token to be rejected")
	}

	other, err := NewSignedURLSigner(base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyz123456")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.Decode(token); ok {
		t.Fatal("expected a token signed with a different key to be rejected")
	}
}

func TestSignedURLSignerRejectsInvalidKey(t *testing.T) {
	if _, err := NewSignedURLSigner(base64.StdEncoding.EncodeToString([]byte("too-short"))); err == nil {
		t.Fatal("expected short key to be rejected")
	}
}
