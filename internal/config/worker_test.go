package config

import (
	"strings"
	"testing"
)

// A deployment that leaves WORKER_ID unset must start, because that is what
// makes `--scale` safe: every replica generates its own consumer name.
func TestValidateProductionWorkerAcceptsGeneratedIdentity(t *testing.T) {
	values := workerEnv()
	if err := ValidateProductionWorker(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("unset WORKER_ID must be accepted: %v", err)
	}
}

// The two fixed names this project previously shipped are rejected, so a copy of
// an old compose file or .env cannot silently reintroduce the collision.
func TestValidateProductionWorkerRejectsLegacyFixedIdentity(t *testing.T) {
	for _, legacy := range []string{"worker-1", "http-worker-1"} {
		values := workerEnv()
		values["WORKER_ID"] = legacy
		err := ValidateProductionWorker(func(name string) string { return values[name] })
		if err == nil {
			t.Fatalf("WORKER_ID=%s was accepted", legacy)
		}
		if !strings.Contains(err.Error(), "WORKER_ID") {
			t.Fatalf("WORKER_ID=%s: error %q does not mention WORKER_ID", legacy, err)
		}
	}
}

func TestWorkerIdentityPrefersExplicitValue(t *testing.T) {
	values := map[string]string{"WORKER_ID": "  pinned-worker  "}
	if got := WorkerIdentity(func(name string) string { return values[name] }); got != "pinned-worker" {
		t.Fatalf("identity=%q, want the trimmed explicit value", got)
	}
}

// Every process must get a distinct name: a shared one makes the consumer group
// treat another replica's in-flight job as stalled and run it a second time.
func TestWorkerIdentityIsUniquePerProcess(t *testing.T) {
	const calls = 64
	seen := make(map[string]bool, calls)
	for range calls {
		identity := WorkerIdentity(func(string) string { return "" })
		if identity == "" {
			t.Fatal("generated an empty identity")
		}
		if seen[identity] {
			t.Fatalf("duplicate identity %q", identity)
		}
		seen[identity] = true
	}
}

func TestWorkerIdentityTreatsBlankAsUnset(t *testing.T) {
	values := map[string]string{"WORKER_ID": "   "}
	if got := WorkerIdentity(func(name string) string { return values[name] }); got == "" {
		t.Fatal("blank WORKER_ID produced an empty identity")
	}
}
