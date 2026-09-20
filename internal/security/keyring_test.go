package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func key(seed byte) string {
	raw := make([]byte, secretKeySize)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// The associated data is authenticated, so changing it makes every stored
// ciphertext fail to open. This value is a golden string on purpose: it is the
// exact input the AEAD is keyed against for monitor_policies.config and
// alert_channels rows, and reintroducing a version into it -- the tempting way
// to record which key sealed a value -- would silently invalidate all of them.
func TestAuthAssociatedDataIsUnchanged(t *testing.T) {
	if got := AuthAssociatedData("tenant-1", "asset-9"); got != "tenant-1:asset-9:auth" {
		t.Fatalf("AuthAssociatedData = %q, want %q", got, "tenant-1:asset-9:auth")
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	keys, err := NewKeyring(key(1))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := keys.Encrypt("sensitive-value", "tenant:channel:url")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "v1:") {
		t.Fatalf("ciphertext is not versioned: %q", encrypted)
	}
	if strings.Contains(encrypted, "sensitive-value") {
		t.Fatal("ciphertext contains plaintext")
	}
	plaintext, err := keys.Decrypt(encrypted, "tenant:channel:url")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "sensitive-value" {
		t.Fatalf("round trip returned %q", plaintext)
	}
	// The associated data still binds the value.
	if _, err := keys.Decrypt(encrypted, "other-tenant:channel:url"); err == nil {
		t.Fatal("ciphertext decrypted under different associated data")
	}
}

// Values written before the keyring existed carry no prefix. They must keep
// working, and the empty string must stay the empty string so a cleared field
// does not become an error.
func TestKeyringReadsLegacyCiphertextAndEmptyValues(t *testing.T) {
	legacy, err := NewSecretCipher(key(1))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := legacy.Encrypt("legacy-value", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, versioned := SplitKeyVersion(encrypted); versioned {
		t.Fatalf("legacy ciphertext looks versioned: %q", encrypted)
	}

	keys, err := NewKeyring(key(1))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := keys.Decrypt(encrypted, "ctx")
	if err != nil {
		t.Fatalf("legacy ciphertext failed to decrypt: %v", err)
	}
	if plaintext != "legacy-value" {
		t.Fatalf("legacy decrypt returned %q", plaintext)
	}

	empty, err := keys.Encrypt("", "ctx")
	if err != nil || empty != "" {
		t.Fatalf("encrypting empty returned %q, %v", empty, err)
	}
	blank, err := keys.Decrypt("", "ctx")
	if err != nil || blank != "" {
		t.Fatalf("decrypting empty returned %q, %v", blank, err)
	}
}

// A rotation that drops a version must fail loudly. Returning the ciphertext or
// an empty string would hand the caller a credential-shaped value that is not
// the credential, and for a webhook secret that means signing alerts with the
// wrong key while everything appears to work.
func TestKeyringRefusesAnUnknownVersion(t *testing.T) {
	keys, err := NewKeyring("v1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = keys.Decrypt("v9:"+base64.RawURLEncoding.EncodeToString([]byte("whatever")), "ctx")
	if err == nil {
		t.Fatal("decrypting an unconfigured key version succeeded")
	}
	if !strings.Contains(err.Error(), "v9") {
		t.Fatalf("error does not name the missing version: %v", err)
	}
}

func TestNewKeyringWritesWithTheHighestVersion(t *testing.T) {
	keys, err := NewKeyring("v1:" + key(1) + ",v3:" + key(3) + ",v2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := keys.Encrypt("value", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "v3:") {
		t.Fatalf("encrypted with %q, want the v3 prefix", encrypted)
	}
	// Every configured version stays readable, in any order in the list.
	for _, material := range []string{"v1:" + key(1), "v2:" + key(2)} {
		other, err := NewKeyring(material)
		if err != nil {
			t.Fatal(err)
		}
		value, err := other.Encrypt("secret", "ctx")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := keys.Decrypt(value, "ctx"); err != nil {
			t.Fatalf("keyring could not read %s: %v", material, err)
		}
	}
}

func TestNewKeyringRejectsMalformedConfiguration(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not base64", "not-a-key"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("too-short"))},
		{"unlabelled version", "1:" + key(1)},
		{"zero version", "v0:" + key(1)},
		{"non-numeric version", "vX:" + key(1)},
		{"duplicate version", "v1:" + key(1) + ",v1:" + key(2)},
		{"bad key in a versioned list", "v1:" + key(1) + ",v2:nope"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewKeyring(testCase.raw); err == nil {
				t.Fatal("malformed key material was accepted")
			}
		})
	}
}

// The signed-URL signer must derive from the same primary key the cipher writes
// with, or a rotation would leave the two on different keys.
func TestPrimaryKeyMaterialMatchesTheSignerDerivation(t *testing.T) {
	keys, err := NewKeyring("v1:" + key(1) + ",v2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	material := keys.PrimaryKeyMaterial()
	// Canonical base64 of the v2 key, whatever form it arrived in.
	if material != key(2) {
		t.Fatalf("PrimaryKeyMaterial = %q, want the canonical encoding of the v2 key", material)
	}
	fromKeyring, err := NewSignedURLSigner(material)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := NewSignedURLSigner(key(2))
	if err != nil {
		t.Fatal(err)
	}
	token := fromKeyring.Encode("payload")
	if _, ok := direct.Decode(token); !ok {
		t.Fatal("signer from the keyring primary does not verify against the primary key")
	}
}

// A plaintext value stored before encryption was introduced has to be
// distinguishable from a versioned one, or a decryption failure would be
// mistaken for a legacy row. A URL contains ':' too, so the prefix has to parse
// as vN rather than merely exist.
func TestSplitKeyVersionOnlyMatchesRealVersions(t *testing.T) {
	if _, _, ok := SplitKeyVersion("https://hooks.example.com/alert"); ok {
		t.Fatal("a plaintext URL was read as a versioned value")
	}
	if _, _, ok := SplitKeyVersion("vmware:something"); ok {
		t.Fatal("a non-numeric label was read as a version")
	}
	version, payload, ok := SplitKeyVersion("v12:abc")
	if !ok || version != 12 || payload != "abc" {
		t.Fatalf("got %d, %q, %v", version, payload, ok)
	}
}
