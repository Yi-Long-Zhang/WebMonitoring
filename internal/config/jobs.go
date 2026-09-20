package config

import "time"

// JobLease returns the configured RUNNING-claim lease, or the store's default
// when JOB_LEASE_SECONDS is unset or unparseable.
//
// The lease is what stops a crashed worker leaving a job RUNNING forever, but it
// is also what lets a peer replica take a job away. ReapTimedOut does not check
// which replica owns a job, so the lease has to exceed the slowest possible
// detection rather than merely outlast a typical one -- see the production gate,
// which enforces the floor that makes that true.
func JobLease(get func(string) string, fallback time.Duration) time.Duration {
	seconds := PositiveInt(get, "JOB_LEASE_SECONDS", 0)
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
