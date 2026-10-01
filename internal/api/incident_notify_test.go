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
	if err := repository.CreateAsset(context.Background(), domain.Asset{
		ID: "asset", TenantID: defaultTenant, Name: "site", URL: "https://site.example",
		Type: "OFFICIAL", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
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
	if err := repository.CreateAsset(context.Background(), domain.Asset{
		ID: "asset", TenantID: defaultTenant, Name: "site", URL: "https://site.example",
		Type: "OFFICIAL", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
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

// TestChannelTestRequiresAnInlineNotifier is the honest-answer case. This test
// server routes notifications through a queue, so there is no dispatcher for a
// test to send through -- and a test that cannot run must say so rather than
// return the success it would have returned had it done something.
func TestChannelTestRequiresAnInlineNotifier(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	if err := repository.CreateAlertChannel(context.Background(), domain.AlertChannel{
		ID: "channel", TenantID: defaultTenant, Name: "ops", Type: "WEBHOOK",
		URL: "https://hooks.example/ops", Enabled: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	response := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels/channel/test", cookie, csrf, "")
	if response.Code == http.StatusOK {
		t.Fatalf("a test with no notifier reported success: %s", response.Body.String())
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("test status=%d body=%s, want 502", response.Code, response.Body.String())
	}
}

// TestChannelTestRejectsAnUnknownChannel: the test must resolve the channel
// within the caller's tenant, so an id that does not exist there is a 404 --
// not an attempt against some other tenant's channel.
func TestChannelTestRejectsAnUnknownChannel(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	response := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels/missing/test", cookie, csrf, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("test status=%d body=%s, want 404", response.Code, response.Body.String())
	}
}
