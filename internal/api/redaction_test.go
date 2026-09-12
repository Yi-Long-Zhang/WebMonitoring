package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorRedactsSensitiveValues(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	writeError(response, request, http.StatusBadRequest, "TEST",
		"Authorization: Bearer response-token",
		map[string]any{
			"password": "response-password",
			"nested":   "Cookie: session=response-cookie",
		},
	)
	body := response.Body.String()
	for _, secret := range []string{"response-token", "response-password", "response-cookie"} {
		if strings.Contains(body, secret) {
			t.Fatalf("error response contains %q: %s", secret, body)
		}
	}
}
