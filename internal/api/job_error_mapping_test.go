package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// internalFailure stands in for a database that is unreachable. The text names
// a host and an address that must never reach a client response.
const internalFailure = "failed to connect to host=db.internal: dial tcp 10.0.0.5:5432: connect: connection refused"

// lookupFailureRepository returns internalFailure for every asset lookup.
type lookupFailureRepository struct {
	store.Repository
}

func (r lookupFailureRepository) GetAsset(_ context.Context, tenantID, id string) (domain.Asset, error) {
	return domain.Asset{}, errors.New(internalFailure)
}

// verifyingWriteFailureRepository delegates everything to the embedded store but
// fails the write that records a successful verification, so the token lookup
// succeeds while persistence does not.
type verifyingWriteFailureRepository struct {
	store.Repository
}

func (r verifyingWriteFailureRepository) UpdateAsset(ctx context.Context, asset domain.Asset) error {
	if asset.VerificationStatus == domain.VerificationVerified {
		return errors.New(internalFailure)
	}
	return r.Repository.UpdateAsset(ctx, asset)
}

// policyFailureRepository serves the asset lookup but fails the policy lookup, so
// a request reaches the policy step and breaks there.
type policyFailureRepository struct {
	store.Repository
}

func (r policyFailureRepository) GetPolicy(_ context.Context, tenantID, assetID string) (domain.MonitorPolicy, error) {
	return domain.MonitorPolicy{}, errors.New(internalFailure)
}

func newVerificationServer(t *testing.T, repository store.Repository, resolver security.Resolver) *Server {
	t.Helper()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	if resolver != nil {
		server.guard.Resolver = resolver
	}
	return server
}

func TestQueueJobMapsErrorsWithoutLeakingInternals(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unknown asset is a 404", func(t *testing.T) {
		server := retentionServer(repository, evidenceStore)
		cookie, csrf := loginForTest(t, server, admin.Username)
		response := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs", cookie, csrf,
			`{"assetId":"does-not-exist","mode":"HTTP"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unknown asset status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("repository failure is a 500 with no internal detail", func(t *testing.T) {
		server := retentionServer(lookupFailureRepository{Repository: repository}, evidenceStore)
		cookie, csrf := loginForTest(t, server, admin.Username)
		response := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs", cookie, csrf,
			`{"assetId":"any","mode":"HTTP"}`)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("repository failure status=%d body=%s", response.Code, response.Body.String())
		}
		if body := response.Body.String(); strings.Contains(body, "db.internal") || strings.Contains(body, "10.0.0.5") {
			t.Fatalf("response leaked repository detail: %s", body)
		}
	})

	t.Run("suspended asset is still rejected as JOB_REJECTED", func(t *testing.T) {
		server := retentionServer(repository, evidenceStore)
		cookie, csrf := loginForTest(t, server, admin.Username)
		// Seeded directly so the SSRF guard does not have to resolve the host.
		target := domain.Asset{
			ID: "suspend-target", TenantID: defaultTenant, Name: "suspended",
			URL: "https://suspended.test", Type: domain.AssetOfficial, OwnerID: admin.ID,
			Enabled: true, VerificationStatus: domain.VerificationVerified,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := repository.CreateAsset(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		if suspend := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+target.ID+"/suspend", cookie, csrf, ""); suspend.Code != http.StatusOK {
			t.Fatalf("suspend status=%d body=%s", suspend.Code, suspend.Body.String())
		}
		response := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs", cookie, csrf,
			`{"assetId":"`+target.ID+`","mode":"HTTP"}`)
		if response.Code != http.StatusConflict {
			t.Fatalf("suspended queue status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "JOB_REJECTED" {
			t.Fatalf("code=%v body=%s", body["code"], response.Body.String())
		}
	})

	t.Run("policy lookup failure is a 500, not a rejection", func(t *testing.T) {
		server := retentionServer(policyFailureRepository{Repository: repository}, evidenceStore)
		cookie, csrf := loginForTest(t, server, admin.Username)
		target := domain.Asset{
			ID: "policy-failure", TenantID: defaultTenant, Name: "policy failure",
			URL: "https://policy.test", Type: domain.AssetOfficial, OwnerID: admin.ID,
			Enabled: true, VerificationStatus: domain.VerificationVerified,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := repository.CreateAsset(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		response := doJSON(t, server, http.MethodPost, "/api/v1/detection-jobs", cookie, csrf,
			`{"assetId":"`+target.ID+`","mode":"HTTP"}`)
		// A broken repository must not be reported as a missing monitoring policy:
		// that would send the operator to fix a configuration problem that is not there.
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("policy failure status=%d body=%s", response.Code, response.Body.String())
		}
		if body := response.Body.String(); strings.Contains(body, "db.internal") || strings.Contains(body, "10.0.0.5") {
			t.Fatalf("response leaked repository detail: %s", body)
		}
	})
}

// A verification whose write fails must not be reported as success: the client
// would otherwise believe an asset is verified while the stored state says
// otherwise.
func TestVerifyAssetReportsWriteFailure(t *testing.T) {
	base := store.NewMemoryRepository()
	admin := retentionUser(t, base, "admin", domain.RoleAdmin)
	resolver := fakeResolver{txt: map[string][]string{}}
	server := newVerificationServer(t, verifyingWriteFailureRepository{Repository: base}, resolver)
	cookie, csrf := loginForTest(t, server, admin.Username)

	asset := domain.Asset{
		ID: "persist-failure", TenantID: defaultTenant, Name: "dns",
		URL: "https://persist.example", Type: domain.AssetOfficial, OwnerID: admin.ID,
		VerificationStatus: domain.VerificationPending, VerificationMethod: domain.VerificationDNS,
	}
	if err := base.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	issue := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"ISSUE"}`)
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var instructions map[string]string
	if err := json.NewDecoder(issue.Body).Decode(&instructions); err != nil {
		t.Fatal(err)
	}
	resolver.txt["_webmonitoring-verify.persist.example"] = []string{instructions["content"]}

	response := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"VERIFY"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("verify with failing write status=%d body=%s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); strings.Contains(body, "db.internal") || strings.Contains(body, "10.0.0.5") {
		t.Fatalf("response leaked repository detail: %s", body)
	}

	stored, err := base.GetAsset(context.Background(), defaultTenant, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.VerificationStatus == domain.VerificationVerified {
		t.Fatalf("stored asset reported verified despite the failed write: %#v", stored)
	}
}
