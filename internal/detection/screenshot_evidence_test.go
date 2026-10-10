package detection

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/store"
)

type stubBrowserChecker struct{ result CheckResult }

func (s stubBrowserChecker) Check(context.Context, domain.Asset, domain.MonitorPolicy) (CheckResult, error) {
	return s.result, nil
}

// evidenceWriteCount reads one series out of the registry's exposition text, so
// the assertion is on the number rather than on the series merely existing.
func evidenceWriteCount(t *testing.T, registry *metrics.Registry, status string) float64 {
	t.Helper()
	key := `webmonitoring_evidence_writes_total{kind="screenshot.png",status="` + status + `"}`
	for _, line := range strings.Split(registry.Snapshot(), "\n") {
		name, value, found := strings.Cut(line, " ")
		if !found || name != key {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return parsed
	}
	return 0
}

// A screenshot the control plane would refuse to read back must not be stored.
//
// The read gate is inclusive and absolute: an object above it is written
// successfully and then rejected by every read path, so downloads answer 404 and
// reports fail on precisely the artefact a tampering incident may hinge on. The
// browser worker budgets against the same number; this is the backstop that
// keeps the stored size trustworthy even if it does not.
func TestScreenshotEvidenceRespectsTheReadLimit(t *testing.T) {
	const tenantID = domain.DefaultTenantID

	cases := []struct {
		name       string
		mode       string
		screenshot []byte
		wantStored bool
		wantStatus string
	}{
		{"full-page screenshot inside the limit is stored", "FULL", []byte("png-bytes"), true, "success"},
		{"viewport fallback is stored too, and says so", "VIEWPORT", []byte("png-bytes"), true, "degraded_viewport"},
		{"omitted screenshot stores nothing, and says so", "OMITTED", nil, false, "omitted"},
		{
			"a screenshot above the limit is refused even though the worker sent it",
			"FULL", make([]byte, evidence.MaxVerifiedBytes+1), false, "oversized",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repository := store.NewMemoryRepository()
			asset := domain.Asset{
				ID: "asset-1", TenantID: tenantID, Name: "target", URL: "https://target.test",
				Type: domain.AssetOfficial, Enabled: true, VerificationStatus: domain.VerificationVerified,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := repository.CreateAsset(ctx, asset); err != nil {
				t.Fatal(err)
			}
			if err := repository.UpsertPolicy(ctx, domain.MonitorPolicy{
				ID: "policy-1", TenantID: tenantID, AssetID: asset.ID, Version: 1,
				HTTPIntervalSeconds: 3600, BrowserIntervalSeconds: 3600, TimeoutSeconds: 30,
				MaxResponseBytes: 5 << 20, IgnoreSelectors: []string{},
			}); err != nil {
				t.Fatal(err)
			}
			job := domain.DetectionJob{
				ID: "job-1", TenantID: tenantID, AssetID: asset.ID, Mode: "BROWSER",
				Status: domain.JobQueued, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
			}
			if err := repository.CreateJob(ctx, job); err != nil {
				t.Fatal(err)
			}

			evidenceStore, err := evidence.NewLocalStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry := metrics.New()
			service := NewService(repository, evidenceStore, nil,
				stubBrowserChecker{result: CheckResult{
					Snapshot:       domain.Snapshot{URL: asset.URL},
					RawHTML:        []byte("<html></html>"),
					Screenshot:     testCase.screenshot,
					ScreenshotMode: testCase.mode,
				}}, nil, events.NewBroker(),
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			service.SetMetrics(registry)

			if err := service.Process(ctx, job); err != nil {
				t.Fatalf("process: %v", err)
			}

			stored, _, err := repository.ListEvidence(ctx, tenantID, asset.ID, store.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range stored {
				if item.Kind == "screenshot.png" {
					found = true
				}
			}
			if found != testCase.wantStored {
				t.Fatalf("screenshot evidence stored=%v, want %v", found, testCase.wantStored)
			}
			// The metric is where a mode becomes visible: VIEWPORT and OMITTED
			// store the same bytes as FULL would in the omitted case, so what
			// distinguishes them is the recorded outcome, and that is what an
			// operator reading the run's history has to go on.
			if got := evidenceWriteCount(t, registry, testCase.wantStatus); got <= 0 {
				t.Fatalf("no evidence write recorded with status %q", testCase.wantStatus)
			}
		})
	}
}
