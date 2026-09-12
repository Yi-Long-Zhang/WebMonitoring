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
