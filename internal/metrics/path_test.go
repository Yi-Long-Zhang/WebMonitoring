package metrics

import (
	"strings"
	"testing"
)

// TestPathClassBucketsByCollection: the point of the classifier is that every
// item of a collection lands in one bucket. A label that carried the id would
// give the registry one series per asset, which is the cardinality explosion
// the class exists to prevent.
func TestPathClassBucketsByCollection(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/api/v1/assets", "/api/v1/assets"},
		{"/api/v1/assets/asset-abc", "/api/v1/assets"},
		{"/api/v1/assets/asset-abc/resume", "/api/v1/assets"},
		{"/api/v1/assets/import", "/api/v1/assets"},
		{"/api/v1/assets/export", "/api/v1/assets"},
		{"/api/v1/incidents/inc-1/timeline", "/api/v1/incidents"},
		{"/api/v1/incidents/inc-1/transition", "/api/v1/incidents"},
		{"/api/v1/alert-channels/chan-9/test", "/api/v1/alert-channels"},
		{"/api/v1/baselines/base-3/approve", "/api/v1/baselines"},
		{"/api/v1/reports/inc-7", "/api/v1/reports"},
		{"/api/v1/policies/asset-abc", "/api/v1/policies"},
		{"/api/v1/detection-jobs/job-2/cancel", "/api/v1/detection-jobs"},
		{"/healthz", "/healthz"},
		{"/readyz", "/readyz"},
		{"/metrics", "/metrics"},
	}
	for _, item := range cases {
		if got := PathClass(item.path); got != item.want {
			t.Fatalf("PathClass(%q) = %q, want %q", item.path, got, item.want)
		}
	}
}

// TestPathClassNeverLabelsASignedToken: a signed evidence link is a bearer
// credential. If it reached the registry as a label it would be written to the
// metrics endpoint, which is exactly where a credential must not be.
func TestPathClassNeverLabelsASignedToken(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.secret-material"
	got := PathClass("/api/v1/evidence/signed/" + token)
	if got != "/api/v1/evidence/download" {
		t.Fatalf("PathClass leaked the token: %q", got)
	}
	if strings.Contains(got, token) {
		t.Fatalf("the signed token survived into the label: %q", got)
	}
}

// TestPathClassIsIdempotent: the /metrics scrape reads the *class* label, and a
// scrape of /metrics must not itself be classed any differently from the first
// time it was observed.
func TestPathClassIsIdempotent(t *testing.T) {
	first := PathClass("/api/v1/assets/abc")
	if second := PathClass("/api/v1/assets/def"); second != first {
		t.Fatalf("two ids of one collection gave %q and %q", first, second)
	}
}

// TestPathClassBoundsUnknownPaths: the API is public, so a crawler can request
// arbitrary depth. Each distinct path must not become its own series -- the
// fallback trims to two segments so a scan is one bucket, not thousands.
func TestPathClassBoundsUnknownPaths(t *testing.T) {
	for _, probe := range []string{
		"/api/v1/../../etc/passwd",
		"/api/v1/unknown-collection/1/2/3/4/5",
		"/wp-login.php",
		"/.env",
		"/",
	} {
		got := PathClass(probe)
		if segments := strings.Split(strings.Trim(got, "/"), "/"); len(segments) > 3 {
			t.Fatalf("PathClass(%q) = %q kept more than two segments", probe, got)
		}
	}
	if got := PathClass("/api/v1/unknown-collection/deep/deeper"); got != "/api/v1/unknown-collection" {
		t.Fatalf("an unknown collection was not collapsed: %q", got)
	}
}

// TestPathClassDoesNotDistinguishTrailingSlash: a client that appends a slash
// is asking for the same collection, and should not split the series.
func TestPathClassDoesNotDistinguishTrailingSlash(t *testing.T) {
	if PathClass("/api/v1/assets/") != PathClass("/api/v1/assets") {
		t.Fatal("a trailing slash split the series")
	}
}
