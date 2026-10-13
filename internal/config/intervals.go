package config

import "time"

// The two background sweeps the control plane runs on a timer. Both are
// documented as being switchable off by setting their interval to 0, and both
// have a manual endpoint (POST /api/v1/maintenance/retention,
// POST /api/v1/maintenance/escalation) that keeps working either way -- so 0 is
// a supported value rather than a mistake, which is why these read through
// NonNegativeInt and the callers guard on > 0.

// RetentionInterval returns the background retention pass interval.
// An explicit 0 disables the timer; unset falls back to once a day.
func RetentionInterval(get func(string) string) time.Duration {
	return time.Duration(NonNegativeInt(get, "RETENTION_INTERVAL_HOURS", 24)) * time.Hour
}

// EscalationInterval returns the unclaimed-incident escalation sweep interval.
// An explicit 0 disables the timer; unset falls back to every 5 minutes.
func EscalationInterval(get func(string) string) time.Duration {
	return time.Duration(NonNegativeInt(get, "ESCALATION_INTERVAL_MINUTES", 5)) * time.Minute
}

// EscalationUnclaimed returns how long a HIGH or CRITICAL incident may stay
// unclaimed before the sweep reports it. Zero is not meaningful here -- an
// incident nobody has had time to look at is not "unclaimed for no time" -- so
// this keeps the PositiveInt behaviour and treats 0 as unset.
func EscalationUnclaimed(get func(string) string) time.Duration {
	return time.Duration(PositiveInt(get, "ESCALATION_UNCLAIMED_MINUTES", 30)) * time.Minute
}
