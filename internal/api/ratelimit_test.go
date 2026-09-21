package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/ratelimit"
	"webmonitoring/internal/security"
)

// limitedServer installs a limiter with the given allowance so a test can reach
// the wall without issuing hundreds of requests.
func limitedServer(t *testing.T, limits ratelimit.Limits) (*Server, *http.Cookie) {
	t.Helper()
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	server.SetRateLimiter(ratelimit.NewMemoryLimiter(limits))
	cookie, _ := loginForTest(t, server, admin.Username)
	return server, cookie
}

func TestRateLimitRefusesPastAllowance(t *testing.T) {
	server, cookie := limitedServer(t, ratelimit.Limits{Read: 2, Write: 1, Bulk: 1, Window: time.Minute})

	get := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if response := get(); response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	refused := get()
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("third request status=%d, want 429", refused.Code)
	}
	// A client that backs off needs a number to back off to; without the header
	// the only sane reaction left is to retry immediately.
	retryAfter := refused.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("429 without a Retry-After header")
	}
	if retryAfter != "60" {
		t.Fatalf("Retry-After=%q, want the window in seconds", retryAfter)
	}
}

// TestRateLimitClassesAreIndependent is the reason the limiter is not one
// counter: a console that polls its dashboard must not consume the allowance for
// the writes and bulk operations an operator needs to actually fix something.
func TestRateLimitClassesAreIndependent(t *testing.T) {
	server, cookie := limitedServer(t, ratelimit.Limits{Read: 1, Write: 2, Bulk: 1, Window: time.Minute})

	read := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	read.AddCookie(cookie)
	readResponse := httptest.NewRecorder()
	server.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("first read status=%d", readResponse.Code)
	}

	// Reads are exhausted; a write must still work.
	write := httptest.NewRequest(http.MethodPost, "/api/v1/sites", nil)
	write.AddCookie(cookie)
	writeResponse := httptest.NewRecorder()
	server.ServeHTTP(writeResponse, write)
	if writeResponse.Code == http.StatusTooManyRequests {
		t.Fatal("exhausting the read allowance also refused a write")
	}
}

// TestRateLimitBulkIsMeteredHardest pins the classification: an asset import and
// a detection trigger fan out into per-item work, so they draw on a much smaller
// allowance than an ordinary read and must not fall back to the write bucket.
func TestRateLimitBulkIsMeteredHardest(t *testing.T) {
	server, cookie := limitedServer(t, ratelimit.Limits{Read: 50, Write: 50, Bulk: 1, Window: time.Minute})

	post := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	// The first import is refused for a different reason (no CSRF token, so 403)
	// or accepted; either way it must not be a 429. The second is the one that
	// must be metered.
	first := post("/api/v1/assets/import")
	if first.Code == http.StatusTooManyRequests {
		t.Fatalf("first bulk request was already rate limited: %s", first.Body.String())
	}
	second := post("/api/v1/assets/import")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second bulk request status=%d, want 429", second.Code)
	}
	// An ordinary read is unaffected by the bulk bucket being empty.
	read := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	read.AddCookie(cookie)
	readResponse := httptest.NewRecorder()
	server.ServeHTTP(readResponse, read)
	if readResponse.Code == http.StatusTooManyRequests {
		t.Fatal("an exhausted bulk allowance refused a read")
	}
}

// TestRateLimitIsPerTenant guards the isolation: one tenant exhausting its
// allowance must not deny another tenant service.
func TestRateLimitIsPerTenant(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	server.SetRateLimiter(ratelimit.NewMemoryLimiter(ratelimit.Limits{Read: 1, Write: 1, Bulk: 1, Window: time.Minute}))
	adminCookie, _ := loginForTest(t, server, admin.Username)

	// A second tenant with its own admin. retentionUser is fixed to the default
	// tenant, so the user is created here rather than through that helper, and
	// the login has to name the tenant slug -- username alone resolves against
	// the default tenant.
	otherSlug := "other-" + identity.New()
	if err := repository.CreateTenant(domain.Tenant{
		ID: identity.New(), Slug: otherSlug, Name: "Other", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	otherTenant, err := repository.GetTenantBySlug(otherSlug)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := security.HashPassword("a-long-password-for-testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateUser(domain.User{
		ID: identity.New(), TenantID: otherTenant.ID, Username: "other-admin",
		PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	otherLogin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session",
		bytes.NewBufferString(`{"tenant":"`+otherSlug+`","username":"other-admin","password":"a-long-password-for-testing"}`))
	otherLogin.Header.Set("Content-Type", "application/json")
	otherLoginResponse := httptest.NewRecorder()
	server.ServeHTTP(otherLoginResponse, otherLogin)
	if otherLoginResponse.Code != http.StatusCreated {
		t.Fatalf("second tenant login status=%d body=%s", otherLoginResponse.Code, otherLoginResponse.Body.String())
	}
	otherCookie := otherLoginResponse.Result().Cookies()[0]

	get := func(cookie *http.Cookie) int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response.Code
	}
	if code := get(adminCookie); code != http.StatusOK {
		t.Fatalf("first tenant read status=%d", code)
	}
	if code := get(adminCookie); code != http.StatusTooManyRequests {
		t.Fatalf("first tenant second read status=%d, want 429", code)
	}
	if code := get(otherCookie); code != http.StatusOK {
		t.Fatalf("second tenant read status=%d, want it unaffected by the first tenant's limit", code)
	}
}

// TestRateLimitAbsentIsUnlimited pins the default. Tests, the embedded
// single-process mode and any deployment that has not configured a limiter must
// keep working: the limiter is an added control, not a prerequisite.
func TestRateLimitAbsentIsUnlimited(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, _ := loginForTest(t, server, admin.Username)
	for attempt := 0; attempt < 20; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d with no limiter installed", attempt, response.Code)
		}
	}
}
