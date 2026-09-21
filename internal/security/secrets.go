package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const secretKeySize = 32

// decodeSecretKey decodes the base64-encoded master secret and enforces the
// expected length. It is shared by the AES-GCM cipher and the signed-URL HMAC.
func decodeSecretKey(encodedKey string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encodedKey)
	}
	if err != nil || len(key) != secretKeySize {
		return nil, errors.New("ALERT_SECRET_KEY must be a base64-encoded 32-byte key")
	}
	return key, nil
}

// SecretBox is what the control plane and the workers depend on to protect
// stored credentials. The concrete implementation is a keyring, so a key can be
// added or retired without every caller holding a version.
type SecretBox interface {
	Encrypt(plaintext, associatedData string) (string, error)
	Decrypt(encoded, associatedData string) (string, error)
}

type SecretCipher struct {
	aead cipher.AEAD
}

func NewSecretCipher(encodedKey string) (*SecretCipher, error) {
	key, err := decodeSecretKey(encodedKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize secret cipher mode: %w", err)
	}
	return &SecretCipher{aead: aead}, nil
}

func (c *SecretCipher) Encrypt(plaintext, associatedData string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(associatedData))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// AuthAssociatedData binds encrypted login credentials to the tenant and asset
// they belong to, so a ciphertext cannot be replayed across assets or tenants.
func AuthAssociatedData(tenantID, assetID string) string {
	return tenantID + ":" + assetID + ":auth"
}

// ChannelAssociatedData binds an alert channel's encrypted URL and secret to the
// row they belong to. The field suffix keeps the two apart: without it the URL
// ciphertext could be substituted for the secret, which for a webhook would mean
// signing with the URL as the HMAC key.
//
// It lives here rather than beside the repository so the key-rotation pass and
// the store derive the same value from one definition. Two copies of this string
// that drifted would look like a lost key at rotation time.
func ChannelAssociatedData(tenantID, channelID, field string) string {
	return tenantID + ":" + channelID + ":" + field
}

func (c *SecretCipher) Decrypt(encoded, associatedData string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return "", errors.New("invalid encrypted secret")
	}
	nonceSize := c.aead.NonceSize()
	plaintext, err := c.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(associatedData))
	if err != nil {
		return "", errors.New("decrypt secret: authentication failed")
	}
	return string(plaintext), nil
}
