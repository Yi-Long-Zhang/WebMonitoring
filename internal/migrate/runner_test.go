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

func TestHasDestructiveDirective(t *testing.T) {
	header := []byte("-- migrate:destructive\n-- deduplicates rows before adding the index.\nUPDATE t SET x = 1;\n")
	if !hasDestructiveDirective(header) {
		t.Fatal("header directive not detected")
	}
	// A mention in later prose must not count.
	prose := []byte("-- Adds an index.\n-- See the migrate:destructive convention for history.\nCREATE INDEX i ON t (x);\n")
	if hasDestructiveDirective(prose) {
		t.Fatal("prose mention was misread as a directive")
	}
	// The first real statement ends the header scan.
	afterStatement := []byte("SELECT 1;\n-- migrate:destructive\n")
	if hasDestructiveDirective(afterStatement) {
		t.Fatal("directive after the first statement must not count")
	}
}

func TestKnownDestructiveMigrationsAreMarked(t *testing.T) {
	files := migrationFiles()
	marked := make(map[string]bool, len(files))
	for _, file := range files {
		marked[file.name] = file.destructive
	}
	// 000012 bulk-CANCELs duplicate active jobs; 000015 merges duplicate
	// incidents and deletes the superseded rows. Both carry sidecar markers
	// because their bytes are frozen by recorded checksums.
	for _, name := range []string{"000012_active_job_uniqueness.up.sql", "000015_incident_reopen.up.sql"} {
		if !marked[name] {
			t.Fatalf("%s is not flagged destructive", name)
		}
	}
	for _, file := range files {
		if file.destructive && file.name != "000012_active_job_uniqueness.up.sql" && file.name != "000015_incident_reopen.up.sql" {
			t.Fatalf("unexpected destructive flag on %s", file.name)
		}
	}
}
