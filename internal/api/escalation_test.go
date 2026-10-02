package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/store"
)

// escalationServer seeds one asset and returns a server holding an incident
// that nobody has claimed. `age` sets how long ago it was created, which is what
// the threshold is measured against.
func escalationServer(t *testing.T, severity domain.Severity, status domain.IncidentStatus, ownerID string, age time.Duration) (*Server, *http.Cookie, string, string, domain.Incident) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	// The address is what makes the escalation reachable at all; without one the
	// pass still reports the incident but has nobody to tell.
	admin.Email = "oncall@example.com"
	if err := repository.UpdateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	asset := domain.Asset{
		ID: identity.New(), TenantID: defaultTenant, Name: "升级用资产",
		URL: "https://escalation.example/", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	baseline := domain.BaselineVersion{
		ID: identity.New(), TenantID: defaultTenant, AssetID: asset.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: admin.ID, CreatedAt: now,
	}
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	created := now.Add(-age)
	incident, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
		ID: identity.New(), TenantID: defaultTenant, AssetID: asset.ID, BaselineID: baseline.ID,
		BaselineVersion: 1, Fingerprint: "escalation-fingerprint", Severity: severity,
		Status: domain.IncidentOpen, Title: "首页被篡改", OwnerID: ownerID,
		Signals: []domain.Signal{{
			Kind: "content", Severity: domain.SeverityHigh, Summary: "changed",
			RuleVersion: "v1", Fingerprint: "escalation-signal",
		}}, OccurrenceCount: 1,
		CreatedAt: created, FirstSeenAt: created, LastSeenAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.IncidentOpen {
		incident.Status = status
		if err := repository.UpdateIncident(context.Background(), incident); err != nil {
			t.Fatal(err)
		}
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)
	return server, cookie, csrf, asset.ID, incident
}

// The failure this covers: a high-risk incident nobody claims produces no signal
// at all, so an installation with nobody watching looks exactly like one with
// nothing to do.
func TestEscalationReportsAnUnclaimedHighRiskIncidentOnce(t *testing.T) {
	server, _, _, _, incident := escalationServer(t, domain.SeverityHigh, domain.IncidentOpen, "", 2*time.Hour)

	reported := server.runEscalation(context.Background(), time.Minute)
	if reported != 1 {
		t.Fatalf("reported = %d, want 1", reported)
	}
	// Once, not once per sweep: a sweep runs on a timer, and repeating the
	// notice until someone claims the incident trains people to ignore it.
	if again := server.runEscalation(context.Background(), time.Minute); again != 0 {
		t.Fatalf("the second sweep reported %d, want 0", again)
	}

	stored, err := server.repository.GetIncident(context.Background(), defaultTenant, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EscalatedAt == nil {
		t.Fatal("the incident was not stamped as escalated")
	}
	audits, _, err := server.repository.ListAudits(context.Background(), defaultTenant,
		store.AuditFilter{Action: "incident.escalated"}, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 {
		t.Fatalf("escalation audit rows = %d, want 1", len(audits))
	}
	// The audit must say how many people were actually reached: "the platform
	// noticed and could not tell anyone" is the case an operator needs to find.
	if notified, ok := audits[0].Details["adminsNotified"]; !ok || notified == nil {
		t.Fatalf("the escalation audit carries no recipient count: %+v", audits[0].Details)
	}
}

// Everything the pass must leave alone: an incident someone owns, one that is
// not high risk, one that has already been through the sweep, and one that is
// simply too new to be late.
func TestEscalationSkipsWhatItShould(t *testing.T) {
	cases := []struct {
		name     string
		severity domain.Severity
		status   domain.IncidentStatus
		age      time.Duration
		owned    bool
	}{
		{name: "already owned", severity: domain.SeverityCritical, status: domain.IncidentOpen, age: 2 * time.Hour, owned: true},
		{name: "medium risk", severity: domain.SeverityMedium, status: domain.IncidentOpen, age: 2 * time.Hour},
		{name: "closed", severity: domain.SeverityCritical, status: domain.IncidentClosed, age: 2 * time.Hour},
		{name: "too new", severity: domain.SeverityCritical, status: domain.IncidentOpen, age: time.Second},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			owner := ""
			if testCase.owned {
				owner = identity.New()
			}
			server, _, _, _, _ := escalationServer(t, testCase.severity, testCase.status, owner, testCase.age)
			if reported := server.runEscalation(context.Background(), time.Minute); reported != 0 {
				t.Fatalf("reported = %d, want 0", reported)
			}
		})
	}
}

// An address that cannot receive is worse than none: it looks configured, so it
// has to be refused at the point it is entered.
func TestUserEmailIsValidated(t *testing.T) {
	server, cookie, csrf, _, _ := escalationServer(t, domain.SeverityLow, domain.IncidentOpen, "", time.Second)
	cases := []struct {
		name  string
		email string
		want  int
	}{
		{"valid", "ops@example.com", http.StatusCreated},
		{"empty is allowed", "", http.StatusCreated},
		{"no at sign", "ops.example.com", http.StatusBadRequest},
		{"nothing after at", "ops@", http.StatusBadRequest},
		{"nothing before at", "@example.com", http.StatusBadRequest},
		{"domain without a dot", "ops@example", http.StatusBadRequest},
		{"contains a space", "ops @example.com", http.StatusBadRequest},
		{"header injection attempt", "ops@example.com\nBcc: x@y.com", http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"username": "user-" + strings.ReplaceAll(testCase.name, " ", "-"),
				"password": "a-long-password-for-testing", "role": "OPERATOR", "email": testCase.email,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status=%d want %d body=%s", response.Code, testCase.want, response.Body.String())
			}
		})
	}
}
