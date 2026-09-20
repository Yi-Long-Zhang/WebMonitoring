package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func auditFilterServer(t *testing.T, events ...domain.AuditEvent) (*Server, *http.Cookie) {
	t.Helper()
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	for _, event := range events {
		if event.TenantID == "" {
			event.TenantID = defaultTenant
		}
		if err := repository.AppendAudit(event); err != nil {
			t.Fatal(err)
		}
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)
	return server, cookie
}

func getAuditEvents(t *testing.T, server *Server, cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events"+query, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// A malformed filter is a 400. Letting it through would either reach Postgres as
// a bad uuid cast (a 500) or silently return a subset the caller did not ask
// for, and both hide the caller's mistake.
func TestAuditFilterRejectsMalformedInput(t *testing.T) {
	server, cookie := auditFilterServer(t)

	cases := []struct {
		name  string
		query string
	}{
		{"actorId is not a uuid", "?actorId=not-a-uuid"},
		{"actorId is too short", "?actorId=1234-5678"},
		{"result is unknown", "?result=MAYBE"},
		{"result is lower case", "?result=success"},
		{"from is not RFC3339", "?from=yesterday"},
		{"to is not RFC3339", "?to=2026-13-45"},
		{"to precedes from", "?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := getAuditEvents(t, server, cookie, testCase.query)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "INVALID_FILTER" {
				t.Fatalf("code=%q body=%s", body.Code, response.Body.String())
			}
		})
	}
}

// A well-formed filter narrows the listing, and the cursor it hands back keeps
// narrowing: the filter has to survive the second page or the walk leaks rows
// the caller excluded.
func TestAuditFilterNarrowsListing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server, cookie := auditFilterServer(t,
		domain.AuditEvent{ID: "11111111-1111-4111-8111-111111111111", Action: "asset.create",
			ResourceType: "asset", ResourceID: "asset-1", Result: "SUCCESS", CreatedAt: now},
		domain.AuditEvent{ID: "22222222-2222-4222-8222-222222222222", Action: "tenant.update",
			ResourceType: "tenant", ResourceID: "tenant-1", Result: "SUCCESS", CreatedAt: now.Add(-time.Minute)},
		domain.AuditEvent{ID: "33333333-3333-4333-8333-333333333333", Action: "asset.create",
			ResourceType: "asset", ResourceID: "asset-2", Result: "FAILED", CreatedAt: now.Add(-2 * time.Minute)},
	)

	response := getAuditEvents(t, server, cookie, "?action=asset.create")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Items []domain.AuditEvent `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("action filter returned %d items: %s", len(page.Items), response.Body.String())
	}
	for _, event := range page.Items {
		if event.Action != "asset.create" {
			t.Fatalf("action filter returned action=%q", event.Action)
		}
	}

	// The same filter with an upper time bound selects only the newest row.
	from := url.QueryEscape(now.Add(-30 * time.Second).Format(time.RFC3339))
	response = getAuditEvents(t, server, cookie, "?action=asset.create&from="+from)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("action+from returned %s", response.Body.String())
	}
}

// The filter is applied before the cursor, so a paged filtered walk returns each
// matching row exactly once and stops when they run out.
func TestAuditFilterHoldsAcrossPages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	events := make([]domain.AuditEvent, 0, 5)
	for i := 0; i < 5; i++ {
		events = append(events, domain.AuditEvent{
			ID:     uuidAt(i),
			Action: "asset.create", ResourceType: "asset",
			ResourceID: "asset", Result: "SUCCESS", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	server, cookie := auditFilterServer(t, events...)

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		query := "?action=asset.create&limit=2"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		response := getAuditEvents(t, server, cookie, query)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var page struct {
			Items      []domain.AuditEvent `json:"items"`
			NextCursor string              `json:"nextCursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Items {
			if seen[event.ID] {
				t.Fatalf("row %s returned twice", event.ID)
			}
			seen[event.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(events) {
		t.Fatalf("paged walk saw %d rows, want %d", len(seen), len(events))
	}
}

// uuidAt builds a distinct, valid uuid per index so the handler's uuid shape
// check and the storage layer both accept it.
func uuidAt(i int) string {
	digits := "0123456789abcdef"
	last := string(digits[i%16])
	return "44444444-4444-4444-8444-4444444444" + last + "0"
}
