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

func TestAssetSuspendAndResume(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	verified := domain.Asset{
		ID: "verified-asset", TenantID: defaultTenant, Name: "verified",
		URL: "https://verified.test", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified,
		CreatedAt: now, UpdatedAt: now,
	}
	pending := domain.Asset{
		ID: "pending-asset", TenantID: defaultTenant, Name: "pending",
		URL: "https://pending.test", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: false, VerificationStatus: domain.VerificationPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAsset(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)

	// Suspending a non-verified asset is an invalid transition.
	suspendPending := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+pending.ID+"/suspend", cookie, csrf, "")
	if suspendPending.Code != http.StatusConflict {
		t.Fatalf("suspend pending status=%d body=%s", suspendPending.Code, suspendPending.Body.String())
	}

	// Suspend the verified asset and confirm it is disabled.
	suspend := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+verified.ID+"/suspend", cookie, csrf, "")
	if suspend.Code != http.StatusOK {
		t.Fatalf("suspend status=%d body=%s", suspend.Code, suspend.Body.String())
	}
	var suspended domain.Asset
	if err := json.NewDecoder(suspend.Body).Decode(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended.VerificationStatus != domain.VerificationSuspended || suspended.Enabled {
		t.Fatalf("unexpected suspended asset: %#v", suspended)
	}

	// A suspended asset cannot be queued for detection.
	run := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs", cookie, csrf,
		`{"assetId":"`+verified.ID+`","mode":"HTTP"}`)
	if run.Code != http.StatusConflict {
		t.Fatalf("detection on suspended asset status=%d body=%s", run.Code, run.Body.String())
	}

	// Resume returns it to VERIFIED but does not re-enable it.
	resume := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+verified.ID+"/resume", cookie, csrf, "")
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	var resumed domain.Asset
	if err := json.NewDecoder(resume.Body).Decode(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.VerificationStatus != domain.VerificationVerified || resumed.Enabled {
		t.Fatalf("unexpected resumed asset: %#v", resumed)
	}

	// Resuming a non-suspended asset is an invalid transition.
	resumeVerified := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+verified.ID+"/resume", cookie, csrf, "")
	if resumeVerified.Code != http.StatusConflict {
		t.Fatalf("resume verified status=%d body=%s", resumeVerified.Code, resumeVerified.Body.String())
	}
}
