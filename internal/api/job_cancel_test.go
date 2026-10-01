package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func TestCancelDetectionJob(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	queued := domain.DetectionJob{
		ID: "queued-job", TenantID: defaultTenant, AssetID: "asset", Mode: "HTTP",
		Status: domain.JobQueued, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: now,
	}
	succeeded := domain.DetectionJob{
		ID: "succeeded-job", TenantID: defaultTenant, AssetID: "asset", Mode: "HTTP",
		Status: domain.JobSucceeded, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: now,
	}
	if err := repository.CreateJob(context.Background(), queued); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateJob(context.Background(), succeeded); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)

	cancel := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs/queued-job/cancel", cookie, csrf, "")
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	var cancelled domain.DetectionJob
	if err := json.NewDecoder(cancel.Body).Decode(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.JobCancelled {
		t.Fatalf("cancelled status=%s, want CANCELLED", cancelled.Status)
	}

	conflict := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs/succeeded-job/cancel", cookie, csrf, "")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("cancel succeeded job status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}
