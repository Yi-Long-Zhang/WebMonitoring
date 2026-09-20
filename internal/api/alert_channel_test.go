package api

import (
	"net/http"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/store"
)

func TestCreateAlertChannelValidation(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
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

// TestUpdateAlertChannelKeepsSecretWhenBlank pins the one rule that makes the
// update safe to submit from a form: the client never receives the stored
// credential, so it sends an empty string back on any field it did not edit.
// If empty meant "erase", every rename would silently break alerting.
func TestUpdateAlertChannelKeepsSecretWhenBlank(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	created := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"hook","type":"WEBHOOK","url":"https://hooks.example.com/a","secret":"s3cret","enabled":true}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var channel domain.AlertChannel
	if err := decodeResponse(created, &channel); err != nil {
		t.Fatal(err)
	}
	if channel.Secret != "" {
		t.Fatalf("create response leaked the secret: %q", channel.Secret)
	}

	// A rename with no secret must not disturb the stored credential.
	renamed := doJSON(t, server, http.MethodPut, "/api/v1/alert-channels/"+channel.ID, cookie, csrf,
		`{"name":"hook renamed","type":"WEBHOOK","url":"https://hooks.example.com/a","enabled":false}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	stored, err := repository.GetAlertChannel(admin.TenantID, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Secret != "s3cret" {
		t.Fatalf("a blank secret wiped the stored credential: %q", stored.Secret)
	}
	if stored.Name != "hook renamed" || stored.Enabled {
		t.Fatalf("update did not apply: name=%q enabled=%v", stored.Name, stored.Enabled)
	}
	// The row keeps its identity, which is what keeps the ciphertext readable.
	if stored.ID != channel.ID {
		t.Fatalf("update changed the row id: %q", stored.ID)
	}

	// A supplied secret replaces it.
	replaced := doJSON(t, server, http.MethodPut, "/api/v1/alert-channels/"+channel.ID, cookie, csrf,
		`{"name":"hook renamed","type":"WEBHOOK","url":"https://hooks.example.com/a","secret":"rotated","enabled":true}`)
	if replaced.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", replaced.Code, replaced.Body.String())
	}
	stored, err = repository.GetAlertChannel(admin.TenantID, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Secret != "rotated" {
		t.Fatalf("a supplied secret was not stored: %q", stored.Secret)
	}
}

// TestUpdateAlertChannelValidationMatchesCreate proves the update path cannot
// be the weaker one. If the SSRF guard only lived in create, a channel could be
// created pointed at a safe address and then repointed at the internal network.
func TestUpdateAlertChannelValidationMatchesCreate(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	created := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"hook","type":"WEBHOOK","url":"https://hooks.example.com/a","enabled":true}`)
	var channel domain.AlertChannel
	if err := decodeResponse(created, &channel); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/alert-channels/" + channel.ID

	for name, body := range map[string]string{
		"unknown type":       `{"name":"hook","type":"SLACK","url":"https://hooks.example.com/a","enabled":true}`,
		"empty name":         `{"name":"  ","type":"WEBHOOK","url":"https://hooks.example.com/a","enabled":true}`,
		"unsafe url":         `{"name":"hook","type":"WEBHOOK","url":"http://127.0.0.1:9000/x","enabled":true}`,
		"email no recipient": `{"name":"hook","type":"EMAIL","enabled":true}`,
	} {
		response := doJSON(t, server, http.MethodPut, path, cookie, csrf, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: update accepted an input create would reject, status=%d body=%s", name, response.Code, response.Body.String())
		}
		// The rejection has to come from a rule, not from a probe that could
		// change underfoot: the fake resolver answers every host with one public
		// address, so the only way `http://127.0.0.1:9000/x` is refused is the
		// port allowlist. Asserting create rejects the very same body is what
		// keeps this test honest if that rule is ever relaxed.
		control := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf, body)
		if control.Code == http.StatusCreated {
			t.Fatalf("%s: create accepted the body the update rejects, so this case proves nothing", name)
		}
	}
}

func TestDeleteAlertChannel(t *testing.T) {
	repository, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	created := doJSON(t, server, http.MethodPost, "/api/v1/alert-channels", cookie, csrf,
		`{"name":"hook","type":"WEBHOOK","url":"https://hooks.example.com/a","enabled":true}`)
	var channel domain.AlertChannel
	if err := decodeResponse(created, &channel); err != nil {
		t.Fatal(err)
	}

	deleted := doJSON(t, server, http.MethodDelete, "/api/v1/alert-channels/"+channel.ID, cookie, csrf, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := repository.GetAlertChannel(admin.TenantID, channel.ID); err != store.ErrNotFound {
		t.Fatalf("channel survived the delete: %v", err)
	}

	// Deleting it again is a not-found, not a silent success, so a mistyped id
	// cannot be mistaken for a completed operation.
	again := doJSON(t, server, http.MethodDelete, "/api/v1/alert-channels/"+channel.ID, cookie, csrf, "")
	if again.Code != http.StatusNotFound {
		t.Fatalf("second delete status=%d body=%s", again.Code, again.Body.String())
	}
}
