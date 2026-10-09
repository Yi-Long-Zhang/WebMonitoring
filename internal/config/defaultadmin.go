package config

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultAdminPasswordFile is the repository's development default for the
// seeded platform administrator. It is a plain file rather than a constant in
// this package so that the compiled binary carries no password: a deployment
// that wants a different default edits this one file, and the value never has
// to be recompiled in.
const defaultAdminPasswordFile = "deployments/compose/.env.default"

// DefaultAdminPassword reports the development default password for the seeded
// platform administrator, or "" when none can be found.
//
// Lookup order, first non-empty wins:
//
//  1. $ADMIN_PASSWORD_FILE -- an explicit path, so a deployment can keep its
//     default outside the repository entirely.
//  2. defaultAdminPasswordFile -- the checked-in development template, searched
//     for from the working directory upwards so the record is found whether the
//     caller runs from the repository root (deployments) or from a package
//     directory (tests).
//
// Returning "" is not an error: the caller decides whether a missing default is
// fatal (the API refuses to start without a password) or merely skips a check
// (the production gate compares against it only when it exists).
func DefaultAdminPassword(get func(string) string) string {
	if path := strings.TrimSpace(get("ADMIN_PASSWORD_FILE")); path != "" {
		if password := passwordFromFile(path); password != "" {
			return password
		}
	}
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		path := filepath.Join(directory, defaultAdminPasswordFile)
		if password := passwordFromFile(path); password != "" {
			return password
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}

// passwordFromFile reads ADMIN_PASSWORD out of a KEY=VALUE file. It parses only
// the one line it needs instead of pulling in a dotenv dependency, and it
// returns "" for a missing or unreadable file so a caller can treat that as
// "no default configured" rather than a startup failure.
func passwordFromFile(path string) string {
	contents, err := os.ReadFile(path) //nolint:gosec // 路径来自运维配置的 ADMIN_PASSWORD_FILE，本就是这个变量的用途
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "ADMIN_PASSWORD" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
