package config

import (
	"testing"
	"time"
)

// TestBackgroundSweepIntervalsCanBeSwitchedOff pins the documented contract that
// setting these intervals to 0 turns the timer off while leaving the manual
// maintenance endpoint in place.
//
// It regressed silently once already: both callers read their interval through
// a parser that substituted the default for any value <= 0, so the guard the
// caller wrote (interval > 0) was never false, the "disabled" branch was dead
// code, and an operator setting 0 to silence the escalation sweep kept getting
// the sweep every 5 minutes. Nothing in the logs or on the console said so --
// the only way to catch it is to assert the value itself.
func TestBackgroundSweepIntervalsCanBeSwitchedOff(t *testing.T) {
	cases := []struct {
		name   string
		read   func(func(string) string) time.Duration
		values map[string]string
		want   time.Duration
	}{
		{"retention unset uses the daily default", RetentionInterval, map[string]string{}, 24 * time.Hour},
		{"retention explicit zero disables", RetentionInterval, map[string]string{"RETENTION_INTERVAL_HOURS": "0"}, 0},
		{"retention honours a real interval", RetentionInterval, map[string]string{"RETENTION_INTERVAL_HOURS": "6"}, 6 * time.Hour},
		{"retention treats a negative as unset", RetentionInterval, map[string]string{"RETENTION_INTERVAL_HOURS": "-1"}, 24 * time.Hour},
		{"retention treats junk as unset", RetentionInterval, map[string]string{"RETENTION_INTERVAL_HOURS": "daily"}, 24 * time.Hour},

		{"escalation unset uses the five-minute default", EscalationInterval, map[string]string{}, 5 * time.Minute},
		{"escalation explicit zero disables", EscalationInterval, map[string]string{"ESCALATION_INTERVAL_MINUTES": "0"}, 0},
		{"escalation honours a real interval", EscalationInterval, map[string]string{"ESCALATION_INTERVAL_MINUTES": "30"}, 30 * time.Minute},
		{"escalation treats a negative as unset", EscalationInterval, map[string]string{"ESCALATION_INTERVAL_MINUTES": "-5"}, 5 * time.Minute},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.read(envFrom(testCase.values)); got != testCase.want {
				t.Fatalf("interval = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestEscalationUnclaimedKeepsZeroAsUnset documents the deliberate asymmetry:
// "unclaimed for zero minutes" is not a policy anyone can want, so 0 here means
// "use the default" rather than "disable".
func TestEscalationUnclaimedKeepsZeroAsUnset(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		want   time.Duration
	}{
		{"unset", map[string]string{}, 30 * time.Minute},
		{"zero", map[string]string{"ESCALATION_UNCLAIMED_MINUTES": "0"}, 30 * time.Minute},
		{"explicit", map[string]string{"ESCALATION_UNCLAIMED_MINUTES": "45"}, 45 * time.Minute},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := EscalationUnclaimed(envFrom(testCase.values)); got != testCase.want {
				t.Fatalf("unclaimed = %v, want %v", got, testCase.want)
			}
		})
	}
}
