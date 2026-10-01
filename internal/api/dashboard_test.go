package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/store"
)

// The dashboard must answer from aggregate queries: seeding history-sized rows
// exercises the same code path the endpoint always takes, and the summary has
// to reflect counts, not page contents.
func TestDashboardReportsAggregateCounts(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()

	verified := domain.Asset{
		ID: "asset-verified", TenantID: defaultTenant, Name: "verified",
		URL: "https://verified.example", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	pending := domain.Asset{
		ID: "asset-pending", TenantID: defaultTenant, Name: "pending",
		URL: "https://pending.example", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: false, VerificationStatus: domain.VerificationPending,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	for _, asset := range []domain.Asset{verified, pending} {
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
	}
	baseline := domain.BaselineVersion{
		ID: identity.New(), TenantID: defaultTenant, AssetID: verified.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: admin.ID, CreatedAt: now,
	}
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	newIncident := func(severity domain.Severity, status domain.IncidentStatus) {
		incident := domain.Incident{
			ID: identity.New(), TenantID: defaultTenant, AssetID: verified.ID,
			BaselineID: baseline.ID, BaselineVersion: 1,
			Fingerprint: string(make([]byte, 64)) + string(severity) + string(status),
			Severity:    severity, Status: status, Title: "changed",
			CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
		}
		if _, _, err := repository.UpsertIncident(context.Background(), incident); err != nil {
			t.Fatal(err)
		}
	}
	newIncident(domain.SeverityCritical, domain.IncidentOpen)
	newIncident(domain.SeverityLow, domain.IncidentOpen)
	newIncident(domain.SeverityHigh, domain.IncidentClosed)
	// History-sized job rows: the dashboard must count these without listing.
	for index := 0; index < 500; index++ {
		job := domain.DetectionJob{
			ID: identity.New(), TenantID: defaultTenant, AssetID: verified.ID, Mode: "HTTP",
			Status: domain.JobSucceeded, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: now.Add(-time.Duration(index) * time.Minute),
		}
		if index%100 == 0 {
			job.Status = domain.JobFailed
		}
		if err := repository.CreateJob(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}

	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)

	response := doJSON(t, server, http.MethodGet, "/api/v1/dashboard", cookie, csrf, "")
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", response.Code, response.Body.String())
	}
	var summary map[string]int
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"assets": 2, "verifiedAssets": 1,
		"openIncidents": 2, "criticalIncidents": 1,
		"failedJobs": 5,
	}
	for key, value := range want {
		if summary[key] != value {
			t.Fatalf("summary[%s] = %d, want %d (full summary %+v)", key, summary[key], value, summary)
		}
	}
}
