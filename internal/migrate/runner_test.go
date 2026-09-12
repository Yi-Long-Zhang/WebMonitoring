package migrate

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestMigrationFilesAreOrderedAndUnique(t *testing.T) {
	files := migrationFiles()
	if len(files) < 4 {
		t.Fatalf("expected at least four migrations, got %d", len(files))
	}
	for index, file := range files {
		if file.version <= 0 {
			t.Fatalf("migration %q has invalid version %d", file.name, file.version)
		}
		if !strings.HasSuffix(file.name, ".up.sql") {
			t.Fatalf("migration %q is not an up migration", file.name)
		}
		if index > 0 && files[index-1].version >= file.version {
			t.Fatalf("migrations are not strictly ordered: %d then %d", files[index-1].version, file.version)
		}
	}
}

func TestMigrationChecksumIsDeterministicSHA256(t *testing.T) {
	first := checksum([]byte("SELECT 1;\n"))
	second := checksum([]byte("SELECT 1;\n"))
	changed := checksum([]byte("SELECT 2;\n"))

	if first != second {
		t.Fatal("same migration content produced different checksums")
	}
	if first == changed {
		t.Fatal("changed migration content produced the same checksum")
	}
	decoded, err := hex.DecodeString(first)
	if err != nil {
		t.Fatalf("checksum is not hexadecimal: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32-byte SHA-256 checksum, got %d bytes", len(decoded))
	}
}
