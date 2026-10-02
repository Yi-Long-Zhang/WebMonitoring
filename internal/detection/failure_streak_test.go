package detection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/notify"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// The failure this guards against is the platform's blind spot about itself:
// when detection breaks there is nothing to see in the console, so an operator
// cannot tell "every page is intact" from "nothing has been fetched for a
// week".
func TestFailureStreakRaisesNoticeAtEveryThreshold(t *testing.T) {
	var notices atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		notices.Add(1)
		var payload struct {
			Event string `json:"event"`
			Data  struct {
				Headline string `json:"headline"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode notice: %v", err)
		}
		if payload.Event != "monitoring.detection_failed" {
			t.Errorf("event = %q, want monitoring.detection_failed", payload.Event)
		}
		if payload.Data.Headline == "" {
			t.Error("notice carried no headline")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repository := store.NewMemoryRepository()
	guard := security.NewURLGuard(true)
	guard.AllowedPorts[strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port)] = struct{}{}
	service := NewService(repository, nil, nil, nil, notify.NewDispatcher(repository, guard),
		events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	seedAsset := func(name string) (string, string) {
		t.Helper()
		suffix := identity.New()
		tenant := domain.Tenant{ID: identity.New(), Slug: "streak-" + suffix, Name: name, CreatedAt: time.Now().UTC()}
		if err := repository.CreateTenant(context.Background(), tenant); err != nil {
			t.Fatal(err)
		}
		owner := domain.User{
			ID: identity.New(), TenantID: tenant.ID, Username: "owner-" + suffix,
			PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
		}
		if err := repository.CreateUser(context.Background(), owner); err != nil {
			t.Fatal(err)
		}
		asset := domain.Asset{
			ID: identity.New(), TenantID: tenant.ID, Name: name, URL: "https://" + suffix + ".example",
			Type: domain.AssetOfficial, OwnerID: owner.ID, Enabled: true,
			VerificationStatus: domain.VerificationVerified, VerificationMethod: domain.VerificationFile,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
		channel := domain.AlertChannel{
			ID: identity.New(), TenantID: tenant.ID, Name: "ops webhook", Type: "WEBHOOK",
			URL: server.URL, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := repository.CreateAlertChannel(context.Background(), channel); err != nil {
			t.Fatal(err)
		}
		return tenant.ID, asset.ID
	}
	// growBy extends the trailing failure run by exactly n and returns nothing:
	// it seeds n-1 terminal failures, then runs the nth check through failJob --
	// the call under test. Each job is a minute apart so the ordering the read
	// depends on is unambiguous.
	growBy := func(tenantID, assetID string, n int) {
		t.Helper()
		created := time.Now().UTC()
		for index := range n - 1 {
			seed := domain.DetectionJob{
				ID: identity.New(), TenantID: tenantID, AssetID: assetID, Mode: "HTTP",
				Status: domain.JobFailed, PolicyVersion: 1, MaxAttempts: 3,
				CreatedAt: created.Add(-time.Duration(index+1) * time.Minute),
			}
			finished := seed.CreatedAt
			seed.FinishedAt = &finished
			if err := repository.CreateJob(context.Background(), seed); err != nil {
				t.Fatal(err)
			}
		}
		job := domain.DetectionJob{
			ID: identity.New(), TenantID: tenantID, AssetID: assetID, Mode: "HTTP",
			Status: domain.JobRunning, PolicyVersion: 1, MaxAttempts: 3, CreatedAt: created,
		}
		if err := repository.CreateJob(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		if err := service.failJob(t.Context(), job, "CHECK_TIMEOUT", errors.New("target timed out")); err != nil {
			t.Fatal(err)
		}
	}

	tenantID, assetID := seedAsset("streak target")
	// Reaching the threshold is the first moment the installation can be sure
	// this is not a blip, so it is the first notice.
	growBy(tenantID, assetID, detectionFailureThreshold)
	if notices.Load() != 1 {
		t.Fatalf("notices after reaching the threshold = %d, want 1", notices.Load())
	}

	// Two further failures sit below the next multiple and must stay quiet: a
	// reminder on every failure would be the noise the threshold exists to
	// avoid.
	growBy(tenantID, assetID, 2)
	if notices.Load() != 1 {
		t.Fatalf("notices between thresholds = %d, want 1 (no reminder below the next multiple)", notices.Load())
	}

	// Reaching the next multiple fires again: an asset that has been
	// unmonitored for twice the threshold is worth repeating.
	growBy(tenantID, assetID, 1)
	if notices.Load() != 2 {
		t.Fatalf("notices at the second threshold = %d, want 2", notices.Load())
	}

	// A run that never reaches the threshold says nothing at all.
	quietTenant, quietAsset := seedAsset("quiet target")
	growBy(quietTenant, quietAsset, detectionFailureThreshold-1)
	if notices.Load() != 2 {
		t.Fatalf("notices after a short run = %d, want 2", notices.Load())
	}
}
