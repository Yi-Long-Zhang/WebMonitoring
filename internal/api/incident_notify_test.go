package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

// recordingAlertQueue captures the alerts the API raises, so a test can assert
// that a hand-made change to an incident reached the notification path at all.
type recordingAlertQueue struct {
	jobs []domain.AlertJob
}

func (q *recordingAlertQueue) EnqueueAlert(_ context.Context, job domain.AlertJob) error {
	q.jobs = append(q.jobs, job)
	return nil
}

// TestTransitionRaisesANotification is the P2-1 claim at the API edge: an
// operator resolving an incident is the end of the loop everyone paged into, and
// the original code notified only when a detection run created an incident --
// so the acknowledgement and the resolution, the two events an on-call engineer
// most wants, were the two that sent nothing.
func TestTransitionRaisesANotification(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	now := time.Now().UTC()
	if err := repository.CreateAsset(domain.Asset{
		ID: "asset", TenantID: defaultTenant, Name: "site", URL: "https://site.example",
		Type: "OFFICIAL", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.UpsertIncident(domain.Incident{
		ID: "incident", TenantID: defaultTenant, AssetID: "asset",
		Severity: domain.SeverityHigh, Status: domain.IncidentOpen,
		Title: "changed", Fingerprint: "fp", OccurrenceCount: 1,
		CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	queue := &recordingAlertQueue{}
	server.detection.SetAlertQueue(queue)

	if response := doJSON(t, server, http.MethodPost, "/api/v1/incidents/incident/transition", cookie, csrf,
		`{"status":"ACKNOWLEDGED"}`); response.Code != http.StatusOK {
		t.Fatalf("transition status=%d body=%s", response.Code, response.Body.String())
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("a status change raised %d notifications, want 1", len(queue.jobs))
	}
	if queue.jobs[0].Reason != domain.AlertReasonTransitioned {
		t.Fatalf("notification reason = %q, want TRANSITIONED", queue.jobs[0].Reason)
	}
	if queue.jobs[0].IncidentID != "incident" || queue.jobs[0].AssetID != "asset" {
		t.Fatalf("notification lost the incident identifiers: %#v", queue.jobs[0])
	}
}

// TestCommentRaisesANotification covers the second new trigger. The note text
// must not travel: a timeline note can carry customer detail and a webhook
// destination is a third party, so the alert says an incident was commented on
// and nothing more.
func TestCommentRaisesANotification(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	now := time.Now().UTC()
	if err := repository.CreateAsset(domain.Asset{
		ID: "asset", TenantID: defaultTenant, Name: "site", URL: "https://site.example",
		Type: "OFFICIAL", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.UpsertIncident(domain.Incident{
		ID: "incident", TenantID: defaultTenant, AssetID: "asset",
		Severity: domain.SeverityHigh, Status: domain.IncidentOpen,
		Title: "changed", Fingerprint: "fp", OccurrenceCount: 1,
		CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	queue := &recordingAlertQueue{}
	server.detection.SetAlertQueue(queue)

	if response := doJSON(t, server, http.MethodPost, "/api/v1/incidents/incident/comments", cookie, csrf,
		`{"note":"checked the page, it is a defacement"}`); response.Code != http.StatusCreated {
		t.Fatalf("comment status=%d body=%s", response.Code, response.Body.String())
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("a comment raised %d notifications, want 1", len(queue.jobs))
	}
	if queue.jobs[0].Reason != domain.AlertReasonCommented {
		t.Fatalf("notification reason = %q, want COMMENTED", queue.jobs[0].Reason)
	}
}
