package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// policyLists decodes the two list fields as raw JSON, which is the only way to
// tell an empty array from null: unmarshalling both into []string yields an
// empty slice either way, and the difference is what broke the console -- it
// called .join() on a null and the policy dialog came up as an error page.
type policyLists struct {
	IgnoreSelectors json.RawMessage `json:"ignoreSelectors"`
	TrustedDomains  json.RawMessage `json:"trustedDomains"`
}

func decodePolicyLists(t *testing.T, body []byte) policyLists {
	t.Helper()
	var decoded policyLists
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode policy: %v (body %s)", err, body)
	}
	return decoded
}

func assertListsAreArrays(t *testing.T, lists policyLists, context string) {
	t.Helper()
	for name, raw := range map[string]json.RawMessage{
		"ignoreSelectors": lists.IgnoreSelectors,
		"trustedDomains":  lists.TrustedDomains,
	} {
		if string(raw) != "[]" {
			t.Errorf("%s: %s is %s, want [] (the contract declares it a required array)", context, name, raw)
		}
	}
}

// The contract declares both fields as required arrays, so a response that says
// null is a broken promise -- and the one consumer that trusted it crashed.
func TestPolicyUpdateAnswersEmptyListsAsArrays(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)

	body := `{"httpIntervalSeconds":300,"browserIntervalSeconds":1800,"timeoutSeconds":30,` +
		`"maxResponseBytes":1048576,"ignoreSelectors":[],"trustedDomains":[]}`
	response := putPolicy(t, server, cookie, csrf, assetID, body)
	if response.Code != http.StatusOK {
		t.Fatalf("update policy: status %d body %s", response.Code, response.Body.String())
	}

	assertListsAreArrays(t, decodePolicyLists(t, response.Body.Bytes()), "update response")
}

// Rows written before the nil-slice trap was understood hold null in their
// stored JSON -- policyServer seeds exactly that shape. They have to come back
// as arrays too, which is why the normalization lives on the response path
// rather than only where the policy is built.
func TestPolicyListNormalizesNullListsFromStoredRows(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)

	response := doJSON(t, server, http.MethodGet, "/api/v1/policies", cookie, csrf, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list policies: status %d body %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Items []policyLists `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list: %v (body %s)", err, response.Body.String())
	}
	if len(envelope.Items) != 1 {
		t.Fatalf("expected the seeded policy, got %d items", len(envelope.Items))
	}
	assertListsAreArrays(t, envelope.Items[0], "list response")

	// The write path is what stops new rows from being stored that way.
	updated := putPolicy(t, server, cookie, csrf, assetID,
		`{"httpIntervalSeconds":600,"browserIntervalSeconds":1800,"timeoutSeconds":30,`+
			`"maxResponseBytes":1048576,"ignoreSelectors":[],"trustedDomains":[]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update policy: status %d body %s", updated.Code, updated.Body.String())
	}
	var stored struct {
		Items []policyLists `json:"items"`
	}
	after := doJSON(t, server, http.MethodGet, "/api/v1/policies", cookie, csrf, "")
	if err := json.Unmarshal(after.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	assertListsAreArrays(t, stored.Items[0], "list response after an update")
}
