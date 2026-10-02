package api

import (
	"context"
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

// reportServer seeds one verified asset carrying an incident whose title is
// hostile, which is the case the report has to survive.
func reportServer(t *testing.T, title string) (*Server, *http.Cookie, domain.Incident) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	now := time.Now().UTC()
	asset := domain.Asset{
		ID: identity.New(), TenantID: defaultTenant, Name: "报告用资产",
		URL: "https://report.example/", Type: domain.AssetOfficial, OwnerID: admin.ID,
		Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateBaseline(context.Background(), domain.BaselineVersion{
		ID: identity.New(), TenantID: defaultTenant, AssetID: asset.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: admin.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	evidenceItem := domain.Evidence{
		ID: identity.New(), TenantID: defaultTenant, AssetID: asset.ID, JobID: identity.New(),
		Kind: "html", MediaType: "text/html", Size: 2048, SHA256: strings.Repeat("a", 64), CreatedAt: now,
	}
	if err := repository.CreateEvidence(context.Background(), evidenceItem); err != nil {
		t.Fatal(err)
	}
	incident, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
		ID: identity.New(), TenantID: defaultTenant, AssetID: asset.ID,
		BaselineID: identity.New(), BaselineVersion: 1, Fingerprint: "report-fingerprint",
		Severity: domain.SeverityHigh, Status: domain.IncidentOpen, Title: title,
		Signals: []domain.Signal{{
			Kind: "content", Severity: domain.SeverityHigh, Summary: title,
			RuleVersion: "v1", Fingerprint: "signal",
		}},
		EvidenceIDs: []string{evidenceItem.ID}, OccurrenceCount: 1,
		CreatedAt: now, FirstSeenAt: now, LastSeenAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	return server, cookie, incident
}

func getReport(t *testing.T, server *Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// The report renders text that came from the monitored page and from whoever
// wrote an incident title. It has to come out escaped rather than interpreted:
// the incident title is attacker-influenced, and an unescaped report is a
// stored-XSS delivery mechanism aimed at the operator.
func TestIncidentReportEscapesUntrustedText(t *testing.T) {
	hostile := `<script>alert(1)</script>`
	server, cookie, incident := reportServer(t, hostile)

	response := getReport(t, server, cookie, "/api/v1/reports/"+incident.ID+"/html")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type = %q, want text/html", contentType)
	}
	body := response.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("the hostile title reached the report unescaped")
	}
	// Escaped, not dropped: a report that silently omitted the text would hide
	// what the page actually said.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("the hostile title is missing from the report entirely")
	}
	// The style block needs a nonce because the global policy is default-src
	// 'none'; without it the report prints unstyled. Scripts stay forbidden,
	// which is the part that matters.
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'nonce-") {
		t.Fatalf("policy = %q, want a nonce for the report's own styles", policy)
	}
	if !strings.Contains(policy, "default-src 'none'") || strings.Contains(policy, "unsafe-inline") {
		t.Fatalf("policy = %q, want no script source and no unsafe-inline", policy)
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "inline") {
		t.Fatalf("disposition = %q, want inline so the report opens rather than downloads", disposition)
	}
}

func TestIncidentReportCarriesTheEvidenceAndTimeline(t *testing.T) {
	server, cookie, incident := reportServer(t, "首页被替换")
	response := getReport(t, server, cookie, "/api/v1/reports/"+incident.ID+"/html")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		"网页篡改监测事件报告", incident.ID, "首页被替换", "报告用资产",
		"检出信号", "处置时间线", "证据清单", incident.EvidenceIDs[0],
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the report does not mention %q", want)
		}
	}
	// The raw snapshot must not be inlined: a stored page is exactly the
	// untrusted markup this report exists to describe, not to embed.
	if strings.Contains(body, "<img") || strings.Contains(body, "<iframe") {
		t.Fatal("the report embedded page markup")
	}
}

func TestIncidentReportAnswersNotFoundForAnUnknownIncident(t *testing.T) {
	server, cookie, _ := reportServer(t, "标题")
	response := getReport(t, server, cookie, "/api/v1/reports/"+identity.New()+"/html")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// The printable summary renders the same aggregate the JSON endpoint returns,
// so the two can never report different numbers for one period.
func TestSummaryReportRendersTheAggregate(t *testing.T) {
	server, cookie, _ := reportServer(t, "标题")
	response := getReport(t, server, cookie, "/api/v1/reports/summary/html")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type = %q, want text/html", contentType)
	}
	body := response.Body.String()
	for _, want := range []string{"网页篡改监测汇总报告", "资产总数", "事件风险分布", "每日事件趋势", "响应时效"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the summary does not mention %q", want)
		}
	}
	// The same rejection rules as the JSON form, because they share the builder.
	badRange := getReport(t, server, cookie, "/api/v1/reports/summary/html?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z")
	if badRange.Code != http.StatusBadRequest {
		t.Fatalf("reversed range status=%d, want 400", badRange.Code)
	}
}
