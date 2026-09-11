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
