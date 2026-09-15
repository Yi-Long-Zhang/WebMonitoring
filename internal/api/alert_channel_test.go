package api

import (
	"net/http"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func TestCreateAlertChannelValidation(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, csrf := loginForTest(t, server, admin.Username)

	// EMAIL without recipients is rejected.
	bad := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"email","type":"EMAIL","enabled":true}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("email without recipients status=%d body=%s", bad.Code, bad.Body.String())
	}

	// EMAIL with recipients succeeds and canonicalizes the recipient list.
	ok := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"email","type":"email","recipients":"a@example.com, b@example.com","enabled":true}`)
	if ok.Code != http.StatusCreated {
		t.Fatalf("email create status=%d body=%s", ok.Code, ok.Body.String())
	}
	var created domain.AlertChannel
	if err := decodeResponse(ok, &created); err != nil {
		t.Fatal(err)
	}
	if created.Recipients != "a@example.com,b@example.com" {
		t.Fatalf("unexpected recipients %q", created.Recipients)
	}

	// Unknown channel types are rejected.
	unknown := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"x","type":"SLACK","url":"https://hooks.example.com/x","enabled":true}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown type status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	// DINGTALK still requires a URL that passes the SSRF guard.
	unsafe := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"dingtalk","type":"DINGTALK","url":"","enabled":true}`)
	if unsafe.Code != http.StatusBadRequest {
		t.Fatalf("dingtalk empty url status=%d body=%s", unsafe.Code, unsafe.Body.String())
	}
}
