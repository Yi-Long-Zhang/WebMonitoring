package detection

import (
	"testing"

	"webmonitoring/internal/domain"
)

func TestCompareEscalatesTwoHighRiskSignals(t *testing.T) {
	before := domain.Snapshot{
		URL:   "https://example.com/",
		Forms: []domain.FormRef{{Action: "https://example.com/login", Method: "POST"}},
	}
	after := domain.Snapshot{
		URL:      "https://example.com/",
		FinalURL: "https://evil.example/phish",
		Scripts:  []domain.ResourceRef{{URL: "https://evil.example/steal.js"}},
		Forms:    []domain.FormRef{{Action: "https://evil.example/submit", Method: "POST"}},
	}
	signals := Compare(before, after, nil, []string{"evidence-id"})
	if len(signals) < 2 {
		t.Fatalf("expected multiple signals, got %#v", signals)
	}
	if OverallSeverity(signals) != domain.SeverityCritical {
		t.Fatalf("expected critical severity, got %s", OverallSeverity(signals))
	}
}

func TestIncidentTransitionRules(t *testing.T) {
	if err := ValidateTransition(domain.IncidentOpen, domain.IncidentAcknowledged); err != nil {
		t.Fatalf("expected OPEN -> ACKNOWLEDGED: %v", err)
	}
	if err := ValidateTransition(domain.IncidentOpen, domain.IncidentClosed); err == nil {
		t.Fatal("expected OPEN -> CLOSED to be rejected")
	}
}

func TestCompareIPAndTLSChanges(t *testing.T) {
	before := domain.Snapshot{
		URL:            "https://example.com/",
		ResolvedIPs:    []string{"203.0.113.10"},
		TLSFingerprint: "before-fingerprint",
	}
	after := domain.Snapshot{
		URL:            "https://example.com/",
		ResolvedIPs:    []string{"203.0.113.20"},
		TLSFingerprint: "after-fingerprint",
	}
	signals := Compare(before, after, nil, []string{"ev"})
	if !hasSignal(signals, "RESOLVED_IP_CHANGED") {
		t.Fatalf("missing RESOLVED_IP_CHANGED in %#v", signals)
	}
	if !hasSignal(signals, "TLS_FINGERPRINT_CHANGED") {
		t.Fatalf("missing TLS_FINGERPRINT_CHANGED in %#v", signals)
	}

	// A missing baseline TLS fingerprint must not trigger a change.
	signals = Compare(domain.Snapshot{URL: "https://example.com/"}, after, nil, nil)
	if hasSignal(signals, "TLS_FINGERPRINT_CHANGED") {
		t.Fatalf("unexpected TLS_FINGERPRINT_CHANGED without baseline: %#v", signals)
	}
}

func TestCompareResourceAndBehaviorSignals(t *testing.T) {
	before := domain.Snapshot{
		URL:              "https://example.com/",
		ResourceRequests: []string{"https://example.com/app.js"},
		ScreenshotHash:   "shot-before",
	}
	after := domain.Snapshot{
		URL:              "https://example.com/",
		ResourceRequests: []string{"https://example.com/app.js", "https://evil.test/x.js"},
		Popups:           []string{"https://evil.test/popup"},
		Dialogs:          []string{"alert"},
		Downloads:        []string{"payload.exe"},
		ScreenshotHash:   "shot-after",
	}
	signals := Compare(before, after, nil, []string{"ev"})
	for _, kind := range []string{"RESOURCE_ADDED", "POPUP_ADDED", "DIALOG_ADDED", "DOWNLOAD_ADDED", "SCREENSHOT_CHANGED"} {
		if !hasSignal(signals, kind) {
			t.Fatalf("missing %s in %#v", kind, signals)
		}
	}
	for _, signal := range signals {
		if signal.Kind == "RESOURCE_ADDED" && signal.Severity != domain.SeverityHigh {
			t.Fatalf("untrusted RESOURCE_ADDED severity=%s, want HIGH", signal.Severity)
		}
	}
}

func hasSignal(signals []domain.Signal, kind string) bool {
	for _, signal := range signals {
		if signal.Kind == kind {
			return true
		}
	}
	return false
}
