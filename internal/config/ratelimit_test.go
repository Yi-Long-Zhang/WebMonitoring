package config

import (
	"strings"
	"testing"
)

// TestRateLimitsAcceptTheDefaultsAndAConfiguredValue pins the boundary the
// validator exists for: an unset variable is fine (the built-in default is a
// real limit), and a deliberately lowered one is fine too.
func TestRateLimitsAcceptTheDefaultsAndAConfiguredValue(t *testing.T) {
	if failures := validateRateLimits(envFrom(nil)); len(failures) != 0 {
		t.Fatalf("unset rate limits were refused: %v", failures)
	}
	if failures := validateRateLimits(envFrom(map[string]string{
		"RATE_LIMIT_READ_PER_WINDOW": "300",
		"RATE_LIMIT_BULK_PER_WINDOW": "4",
	})); len(failures) != 0 {
		t.Fatalf("a plausible configuration was refused: %v", failures)
	}
}

// TestRateLimitsRefuseZero is the important one. Zero is the value that turns a
// class off, so a production operator who typed 0 meaning "use the default" would
// otherwise end up with no limit at all while believing they had set one -- the
// exact opposite of what they asked for, and silently.
func TestRateLimitsRefuseZero(t *testing.T) {
	for _, name := range []string{
		"RATE_LIMIT_READ_PER_WINDOW",
		"RATE_LIMIT_WRITE_PER_WINDOW",
		"RATE_LIMIT_BULK_PER_WINDOW",
	} {
		failures := validateRateLimits(envFrom(map[string]string{name: "0"}))
		if len(failures) != 1 {
			t.Fatalf("%s=0 produced %d failures, want exactly one", name, len(failures))
		}
		if !strings.Contains(failures[0].Error(), "disables the limit") {
			t.Fatalf("%s=0 failure does not explain itself: %v", name, failures[0])
		}
	}
}

func TestRateLimitsRefuseNegativeAndNonInteger(t *testing.T) {
	if failures := validateRateLimits(envFrom(map[string]string{"RATE_LIMIT_WRITE_PER_WINDOW": "-1"})); len(failures) != 1 {
		t.Fatalf("a negative allowance was accepted: %v", failures)
	}
	if failures := validateRateLimits(envFrom(map[string]string{"RATE_LIMIT_WRITE_PER_WINDOW": "lots"})); len(failures) != 1 {
		t.Fatalf("a non-integer allowance was accepted: %v", failures)
	}
}

// TestRateLimitsRefuseAnEffectivelyUnlimitedValue: the whole point of the control
// is that one tenant cannot consume the installation, so a ceiling high enough to
// never bite protects nothing and is indistinguishable from leaving it unset.
func TestRateLimitsRefuseAnEffectivelyUnlimitedValue(t *testing.T) {
	failures := validateRateLimits(envFrom(map[string]string{"RATE_LIMIT_READ_PER_WINDOW": "1000001"}))
	if len(failures) != 1 {
		t.Fatalf("an effectively unlimited read allowance was accepted: %v", failures)
	}
	// The default sits under its own ceiling; if it ever stopped doing so the
	// validator would refuse every deployment that left the variable alone.
	if failures := validateRateLimits(envFrom(nil)); len(failures) != 0 {
		t.Fatalf("the built-in defaults do not satisfy the validator: %v", failures)
	}
}

// TestRateLimitsAreCheckedEvenOutsideProduction documents the reach of the check
// rather than a second code path: validateCommon is the shared tail of both the
// API and the worker validation, so a deployment cannot skip it by running only
// one of the two tiers.
func TestRateLimitsAreCheckedEvenOutsideProduction(t *testing.T) {
	failures := validateCommon(envFrom(map[string]string{"RATE_LIMIT_BULK_PER_WINDOW": "0"}))
	found := false
	for _, failure := range failures {
		if strings.Contains(failure.Error(), "RATE_LIMIT_BULK_PER_WINDOW") {
			found = true
		}
	}
	if !found {
		t.Fatalf("validateCommon did not check the rate limits: %v", failures)
	}
}

// TestAlertGateRefusesADisabledControl: 0 turns a notification control off, and
// an operator who typed it meaning "use the default" would otherwise ship an
// installation with no runaway guard and no breaker while believing they had
// configured both. Unlike the request limiter there is no ceiling -- a busy
// installation may legitimately want a large allowance.
func TestAlertGateRefusesADisabledControl(t *testing.T) {
	for _, name := range []string{"ALERT_MAX_PER_CHANNEL_PER_WINDOW", "ALERT_FAILURE_THRESHOLD"} {
		failures := validateAlertGate(envFrom(map[string]string{name: "0"}))
		if len(failures) != 1 {
			t.Fatalf("%s=0 produced %d failures, want exactly one", name, len(failures))
		}
		if !strings.Contains(failures[0].Error(), "disables") {
			t.Fatalf("%s=0 failure does not explain itself: %v", name, failures[0])
		}
	}
	if failures := validateAlertGate(envFrom(map[string]string{"ALERT_GATE_WINDOW_SECONDS": "0"})); len(failures) != 1 {
		t.Fatalf("a zero window was accepted: %v", failures)
	}
}

func TestAlertGateAcceptsUnsetAndConfiguredValues(t *testing.T) {
	if failures := validateAlertGate(envFrom(nil)); len(failures) != 0 {
		t.Fatalf("unset alert gate variables were refused: %v", failures)
	}
	// A large allowance is legitimate here: only disabling is refused.
	if failures := validateAlertGate(envFrom(map[string]string{
		"ALERT_MAX_PER_CHANNEL_PER_WINDOW": "100000",
		"ALERT_FAILURE_THRESHOLD":          "10",
		"ALERT_GATE_WINDOW_SECONDS":        "300",
	})); len(failures) != 0 {
		t.Fatalf("a plausible configuration was refused: %v", failures)
	}
	// The defaults must satisfy the validator, or every deployment that left the
	// variables alone would be refused.
	if failures := validateAlertGate(envFrom(map[string]string{
		"ALERT_MAX_PER_CHANNEL_PER_WINDOW": "60",
		"ALERT_FAILURE_THRESHOLD":          "5",
	})); len(failures) != 0 {
		t.Fatalf("the built-in defaults do not satisfy the validator: %v", failures)
	}
}
