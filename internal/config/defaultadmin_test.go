package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultAdminPasswordPrefersExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(path, []byte("ADMIN_USERNAME=ops\nADMIN_PASSWORD=custom-default-pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	get := func(name string) string {
		if name == "ADMIN_PASSWORD_FILE" {
			return path
		}
		return ""
	}
	if got := DefaultAdminPassword(get); got != "custom-default-pw" {
		t.Fatalf("ADMIN_PASSWORD_FILE was not honoured: got %q", got)
	}
}

// A missing file is reported as "no default", not as an error: the API treats it
// as fatal on its own, and the production gate only needs to skip its check.
func TestDefaultAdminPasswordIsEmptyWithoutAFile(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	if got := DefaultAdminPassword(func(string) string { return "" }); got != "" {
		t.Fatalf("expected no default outside the repository, got %q", got)
	}
}

func TestDefaultAdminPasswordParsesQuotesAndSpacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quoted.env")
	// Surrounding quotes and padding are stripped, and a same-named line with a
	// different key must not be mistaken for the password.
	if err := os.WriteFile(path, []byte("  ADMIN_PASSWORD = \"padded-secret\"  \nADMIN_PASSWORD_HINT=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	get := func(name string) string {
		if name == "ADMIN_PASSWORD_FILE" {
			return path
		}
		return ""
	}
	// The key compares after trimming, so the padded line matches; the hint key
	// does not, because the comparison is on the whole trimmed key.
	if got := DefaultAdminPassword(get); got != "padded-secret" {
		t.Fatalf("expected the padded value to be parsed, got %q", got)
	}
}
