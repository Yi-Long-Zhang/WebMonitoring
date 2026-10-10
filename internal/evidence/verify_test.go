package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
)

type memoryStore struct {
	content []byte
}

func (m memoryStore) Put(string, string, string, string, string, []byte) (domain.Evidence, error) {
	panic("not implemented")
}

func (m memoryStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(m.content))), nil
}

func (memoryStore) Delete(string) error {
	return nil
}

func TestReadVerifiedRejectsTamperedContent(t *testing.T) {
	content := []byte("original evidence")
	sum := sha256.Sum256(content)
	item := domain.Evidence{
		Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), ObjectKey: "evidence",
	}

	if _, err := ReadVerified(context.Background(), memoryStore{content: []byte("tampered")}, item); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestReadVerifiedReturnsExactContent(t *testing.T) {
	content := []byte("original evidence")
	sum := sha256.Sum256(content)
	item := domain.Evidence{
		Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), ObjectKey: "evidence",
	}

	got, err := ReadVerified(context.Background(), memoryStore{content: content}, item)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("unexpected content %q", got)
	}
}

// The limit is inclusive, and both sides of it are asserted. An off-by-one here
// is what decides whether an object is merely large or permanently unreadable,
// and the failing direction is silent: the write succeeds either way.
func TestReadVerifiedBoundaryAtTheLimit(t *testing.T) {
	content := []byte("original evidence")
	sum := sha256.Sum256(content)
	over := domain.Evidence{
		Size: MaxVerifiedBytes + 1, SHA256: hex.EncodeToString(sum[:]), ObjectKey: "evidence",
	}
	if _, err := ReadVerified(context.Background(), memoryStore{content: content}, over); err == nil {
		t.Fatal("an object above the limit must be refused before it is read")
	}

	atLimit := make([]byte, MaxVerifiedBytes)
	limitSum := sha256.Sum256(atLimit)
	exact := domain.Evidence{
		Size: int64(len(atLimit)), SHA256: hex.EncodeToString(limitSum[:]), ObjectKey: "evidence",
	}
	got, err := ReadVerified(context.Background(), memoryStore{content: atLimit}, exact)
	if err != nil {
		t.Fatalf("an object exactly at the limit must be readable: %v", err)
	}
	if len(got) != MaxVerifiedBytes {
		t.Fatalf("read %d bytes, want %d", len(got), MaxVerifiedBytes)
	}
}
