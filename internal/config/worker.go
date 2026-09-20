package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"
)

// legacyWorkerIDs are the fixed consumer names a worker took before the
// identity was generated per process: the built-in default and the value the
// compose file pinned. Both identify a deployment rather than a process, so
// every replica of a scaled worker tier shares one, and the consumer group then
// treats another replica's in-flight job as a stalled message and reclaims it.
var legacyWorkerIDs = map[string]bool{
	"worker-1":      true,
	"http-worker-1": true,
}

// WorkerIdentity returns the Redis consumer name for this process.
//
// The name must be unique per running instance. internal/jobqueue reclaims
// messages another consumer has held longer than QUEUE_MIN_IDLE_SECONDS, so two
// workers sharing a name steal each other's in-flight jobs and run them twice.
// With WORKER_ID unset the identity is derived from the hostname and a random
// suffix, which is what makes `docker compose --scale http-worker=3` safe: each
// container has its own hostname.
//
// WORKER_ID still wins when set, so a single instance can take a stable name for
// log and metric correlation. ValidateProductionWorker rejects the two legacy
// fixed values, because pinning one of those is exactly what caused the
// duplicate processing this function exists to prevent.
func WorkerIdentity(get func(string) string) string {
	if explicit := strings.TrimSpace(get("WORKER_ID")); explicit != "" {
		return explicit
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "worker"
	}
	return host + "-" + randomSuffix()
}

func randomSuffix() string {
	var buffer [6]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		// The identity only has to be distinct, not unpredictable, so falling
		// back to the clock keeps the worker starting when entropy is
		// unavailable instead of failing the whole process.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buffer[:])
}
