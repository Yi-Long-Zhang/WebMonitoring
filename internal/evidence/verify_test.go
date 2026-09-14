package evidence

import (
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

func (m memoryStore) Open(string) (io.ReadCloser, error) {
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

	if _, err := ReadVerified(memoryStore{content: []byte("tampered")}, item); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestReadVerifiedReturnsExactContent(t *testing.T) {
	content := []byte("original evidence")
	sum := sha256.Sum256(content)
	item := domain.Evidence{
		Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), ObjectKey: "evidence",
	}

	got, err := ReadVerified(memoryStore{content: content}, item)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("unexpected content %q", got)
	}
}
