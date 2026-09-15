package identity

import (
	"regexp"
	"strings"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewReturnsRFC4122Version4UUIDs(t *testing.T) {
	for range 64 {
		id := New()
		if !uuidPattern.MatchString(id) {
			t.Fatalf("id %q is not a lowercase version-4 UUID", id)
		}
		if parts := strings.Split(id, "-"); len(parts) != 5 {
			t.Fatalf("id %q has %d groups, want 5", id, len(parts))
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	const count = 20000
	seen := make(map[string]struct{}, count)
	for i := range count {
		id := New()
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate id generated at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}
