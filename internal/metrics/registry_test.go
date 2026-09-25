package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestRegistryRendersPrometheusLabelsAndHistogram(t *testing.T) {
	registry := New()
	registry.Inc("webmonitoring_requests_total", map[string]string{
		"method": "GET", "path": `/quote/"line`,
	})
	registry.ObserveDuration("webmonitoring_request_duration_seconds", 20*time.Millisecond, nil)

	body := registry.Snapshot()
	if !strings.Contains(body, `webmonitoring_requests_total{method="GET",path="/quote/\"line"} 1`) {
		t.Fatalf("missing escaped counter: %s", body)
	}
	if !strings.Contains(body, "webmonitoring_request_duration_seconds_bucket") ||
		!strings.Contains(body, "webmonitoring_request_duration_seconds_count 1") {
		t.Fatalf("missing histogram: %s", body)
	}
}

func TestRegistryRedactsSensitiveLabelValues(t *testing.T) {
	registry := New()
	registry.Inc("webmonitoring_test_total", map[string]string{
		"token": "plain-token",
		"error": "Authorization: Bearer bearer-token",
	})
	body := registry.Snapshot()
	for _, secret := range []string{"plain-token", "bearer-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metric output contains %q: %s", secret, body)
		}
	}
}

// A collector runs on the scrape path, so a value that is cheaper to read on
// demand than to keep fresh -- the connection pool's counters -- is reported as
// it is now rather than as it was at some earlier tick. It also has to be able
// to call Set, which takes the same lock the handler holds while rendering; a
// collector that deadlocked here would hang every scrape.
func TestRegistryRunsCollectorsAtScrapeTime(t *testing.T) {
	registry := New()
	reads := 0
	registry.OnCollect(func(collected *Registry) {
		reads++
		collected.Set("webmonitoring_test_pool_connections", float64(reads), nil)
	})

	if body := registry.Snapshot(); !strings.Contains(body, "webmonitoring_test_pool_connections 1") {
		t.Fatalf("collector did not run at scrape time: %s", body)
	}
	if body := registry.Snapshot(); !strings.Contains(body, "webmonitoring_test_pool_connections 2") {
		t.Fatalf("collector did not run again on the next scrape: %s", body)
	}
}
