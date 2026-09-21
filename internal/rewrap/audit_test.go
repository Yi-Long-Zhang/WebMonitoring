package rewrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
)

// auditRecorder captures what RecordAudit wrote.
type auditRecorder struct {
	events []domain.AuditEvent
	err    error
}

func (a *auditRecorder) AppendAudit(event domain.AuditEvent) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, event)
	return nil
}

func TestRecordAuditSummarizesWithoutCredentials(t *testing.T) {
	recorder := &auditRecorder{}
	stats := Stats{
		ChannelsScanned: 3, ChannelsRewrapped: 2,
		PoliciesScanned: 4, PoliciesRewrapped: 1, AlreadyCurrent: 4,
	}
	if err := RecordAudit(context.Background(), recorder, 2, false, stats, nil); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Action != Action || event.Result != "SUCCESS" {
		t.Fatalf("event = %+v, want action %q and SUCCESS", event, Action)
	}
	if event.TenantID != domain.DefaultTenantID {
		t.Fatalf("TenantID = %q, want the platform sentinel", event.TenantID)
	}
	if event.ActorID != "" {
		t.Fatalf("ActorID = %q, want it left empty: a command line has no principal", event.ActorID)
	}
	for _, key := range []string{"channelsRewrapped", "policiesRewrapped", "primaryVersion", "dryRun"} {
		if _, ok := event.Details[key]; !ok {
			t.Fatalf("details = %v, want %q", event.Details, key)
		}
	}
	if event.Details["channelsRewrapped"] != 2 || event.Details["primaryVersion"] != 2 {
		t.Fatalf("details = %v, want the counts and the version", event.Details)
	}
	// The whole point of recording counts rather than values: nothing in the
	// event may let a reader reconstruct a credential.
	for key, value := range event.Details {
		text, ok := value.(string)
		if !ok {
			continue
		}
		for _, forbidden := range []string{"v1:", "v2:", "secret", "password"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Fatalf("details[%q] = %q, which looks like credential material", key, text)
			}
		}
	}
}

func TestRecordAuditMarksPartialAndFailed(t *testing.T) {
	partial := &auditRecorder{}
	stats := Stats{Failures: []string{"alert channel x: decrypt: key not configured"}}
	if err := RecordAudit(context.Background(), partial, 2, false, stats, errors.New("1 value(s) could not be rewrapped")); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if partial.events[0].Result != "FAILED" {
		t.Fatalf("result = %q, want FAILED when the pass returned an error", partial.events[0].Result)
	}

	// A pass that finished but left rows behind is PARTIAL, not SUCCESS: that is
	// the state an operator has to be able to find.
	incomplete := &auditRecorder{}
	if err := RecordAudit(context.Background(), incomplete, 2, false, stats, nil); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if incomplete.events[0].Result != "PARTIAL" {
		t.Fatalf("result = %q, want PARTIAL with failures and no error", incomplete.events[0].Result)
	}
}

func TestRecordAuditLabelsDryRuns(t *testing.T) {
	recorder := &auditRecorder{}
	if err := RecordAudit(context.Background(), recorder, 2, true, Stats{ChannelsRewrapped: 1}, nil); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if recorder.events[0].Details["dryRun"] != true {
		t.Fatalf("details = %v, want dryRun true", recorder.events[0].Details)
	}
}
