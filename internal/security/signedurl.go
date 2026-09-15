package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

// SignedURLSigner mints and verifies self-contained HMAC-signed tokens used for
// short-lived evidence download URLs served by the control plane (the local
// evidence store path). The signing key is derived from ALERT_SECRET_KEY with a
// domain-separation label so it is never reused for the AES-GCM cipher.
type SignedURLSigner struct {
	key []byte
}

func NewSignedURLSigner(encodedKey string) (*SignedURLSigner, error) {
	master, err := decodeSecretKey(encodedKey)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("webmonitoring:evidence-signed-url:v1"))
	return &SignedURLSigner{key: mac.Sum(nil)}, nil
}

// Encode returns a base64url token carrying `message` plus its HMAC signature.
func (s *SignedURLSigner) Encode(message string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(message))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(message)) + "." + signature
}

// Decode verifies the token's signature and returns the original message, or
// ok=false when the token is malformed or its signature does not match.
func (s *SignedURLSigner) Decode(token string) (message string, ok bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(raw)
	expected := mac.Sum(nil)
	if len(signature) != len(expected) || subtle.ConstantTimeCompare(signature, expected) != 1 {
		return "", false
	}
	return string(raw), true
}
