package security

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Keyring holds every key the deployment can still read plus the one to write
// with, and stamps a version onto each ciphertext it produces.
//
// Without a version there is no rotation path at all: the ciphertext carries no
// record of which key sealed it, so changing ALERT_SECRET_KEY makes every stored
// credential undecryptable and there is no way to tell that apart from a wrong
// key. The version lives in the ciphertext prefix rather than in the associated
// data, because the associated data is authenticated: adding a version to it
// would change the AEAD input and break every existing ciphertext, which is the
// failure this type exists to prevent.
type Keyring struct {
	boxes   map[int]*SecretCipher
	primary int
	// material is the primary key re-encoded, for callers that must derive their
	// own key from the same one (the signed-URL HMAC). Expressing the rotation in
	// one place is what keeps the two in step.
	material string
}

// NewKeyring accepts either key form, so no existing deployment, environment
// variable or test changes meaning:
//
//	ALERT_SECRET_KEYS="v1:<base64-32B>,v2:<base64-32B>"  -- highest N is primary
//	ALERT_SECRET_KEY="<base64-32B>"                      -- equivalent to v1:<key>
//
// The labels are explicit rather than positional on purpose: positional versions
// would silently renumber if anyone reordered the list, and a silent renumber is
// a silent decryption failure.
func NewKeyring(raw string) (*Keyring, error) {
	keys := &Keyring{boxes: map[int]*SecretCipher{}}
	// Material is kept per version only so the primary's can be handed back
	// re-encoded; the boxes themselves never expose their key.
	materials := map[int]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		version := 1
		material := entry
		// A base64 key can never contain ':' in either alphabet, so its presence
		// always means an explicit label.
		if label, rest, found := strings.Cut(entry, ":"); found {
			parsed, err := parseKeyVersion(label)
			if err != nil {
				return nil, err
			}
			version, material = parsed, rest
		}
		if _, exists := keys.boxes[version]; exists {
			return nil, fmt.Errorf("secret key version v%d is configured more than once", version)
		}
		box, err := NewSecretCipher(material)
		if err != nil {
			return nil, err
		}
		keys.boxes[version] = box
		materials[version] = material
		if version > keys.primary {
			keys.primary = version
		}
	}
	if len(keys.boxes) == 0 {
		return nil, errors.New("ALERT_SECRET_KEY must be a base64-encoded 32-byte key")
	}
	// Re-encode the primary from its decoded bytes so the caller always receives
	// canonical base64, whatever form the entry arrived in.
	decoded, err := decodeSecretKey(materials[keys.primary])
	if err != nil {
		return nil, err
	}
	keys.material = base64.StdEncoding.EncodeToString(decoded)
	return keys, nil
}

// PrimaryKeyMaterial returns the primary key re-encoded, so a caller that has to
// derive its own subkey does so from the same key Encrypt writes with.
func (k *Keyring) PrimaryKeyMaterial() string {
	return k.material
}

func (k *Keyring) Encrypt(plaintext, associatedData string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	sealed, err := k.boxes[k.primary].Encrypt(plaintext, associatedData)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d:%s", k.primary, sealed), nil
}

func (k *Keyring) Decrypt(encoded, associatedData string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	version, payload, versioned := SplitKeyVersion(encoded)
	if !versioned {
		// Unversioned means it predates the keyring; such a value was written by
		// what is now v1.
		version = 1
	}
	box, ok := k.boxes[version]
	if !ok {
		return "", fmt.Errorf("secret was encrypted with key version v%d, which is not configured", version)
	}
	return box.Decrypt(payload, associatedData)
}

// SplitKeyVersion separates a versioned ciphertext into its key version and its
// payload. It reports ok=false for a value with no version prefix, which is both
// the legacy ciphertext shape and the shape of a value stored before encryption
// was introduced at all -- a distinction the caller has to make, so this must
// not guess. A plaintext URL contains ':' too, which is why the label has to
// parse as vN rather than merely exist.
func SplitKeyVersion(encoded string) (version int, payload string, ok bool) {
	label, rest, found := strings.Cut(encoded, ":")
	if !found || !strings.HasPrefix(label, "v") {
		return 0, encoded, false
	}
	parsed, err := strconv.Atoi(label[1:])
	if err != nil || parsed < 1 {
		return 0, encoded, false
	}
	return parsed, rest, true
}

func parseKeyVersion(label string) (int, error) {
	if !strings.HasPrefix(label, "v") {
		return 0, fmt.Errorf("secret key label %q must look like v1, v2, ...", label)
	}
	version, err := strconv.Atoi(label[1:])
	if err != nil || version < 1 {
		return 0, fmt.Errorf("secret key label %q must look like v1, v2, ...", label)
	}
	return version, nil
}
