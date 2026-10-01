package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// fakeResolver returns a fixed public IP for any host and serves TXT records
// from an in-memory map so DNS verification can be exercised without network.
type fakeResolver struct {
	txt map[string][]string
}

func (r fakeResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}

func (r fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return r.txt[name], nil
}

func verificationServer(t *testing.T, resolver security.Resolver) (*store.MemoryRepository, *Server, domain.User) {
	t.Helper()
	repository := store.NewMemoryRepository()
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	if resolver != nil {
		server.guard.Resolver = resolver
	}
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	return repository, server, admin
}

func TestCreateAssetVerificationMethod(t *testing.T) {
	_, server, admin := verificationServer(t, fakeResolver{txt: map[string][]string{}})
	cookie, csrf := loginForTest(t, server, admin.Username)

	t.Run("defaults to FILE", func(t *testing.T) {
		response := doJSON(t, server, http.MethodPost, "/api/v1/assets", cookie, csrf,
			`{"name":"default","url":"https://default.example","type":"OFFICIAL"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
		var asset domain.Asset
		if err := json.NewDecoder(response.Body).Decode(&asset); err != nil {
			t.Fatal(err)
		}
		if asset.VerificationMethod != domain.VerificationFile {
			t.Fatalf("expected FILE, got %q", asset.VerificationMethod)
		}
	})

	t.Run("accepts DNS", func(t *testing.T) {
		response := doJSON(t, server, http.MethodPost, "/api/v1/assets", cookie, csrf,
			`{"name":"dns","url":"https://dns.example","type":"OFFICIAL","verificationMethod":"DNS"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
		var asset domain.Asset
		if err := json.NewDecoder(response.Body).Decode(&asset); err != nil {
			t.Fatal(err)
		}
		if asset.VerificationMethod != domain.VerificationDNS {
			t.Fatalf("expected DNS, got %q", asset.VerificationMethod)
		}
	})

	t.Run("rejects unknown method", func(t *testing.T) {
		response := doJSON(t, server, http.MethodPost, "/api/v1/assets", cookie, csrf,
			`{"name":"bad","url":"https://bad.example","type":"OFFICIAL","verificationMethod":"NOPE"}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestVerificationIssueInstructionsByMethod(t *testing.T) {
	repository, server, admin := verificationServer(t, nil)
	cookie, csrf := loginForTest(t, server, admin.Username)

	cases := []struct {
		method      domain.VerificationMethod
		url         string
		expectKey   string
		expectValue string
	}{
		{domain.VerificationFile, "https://file.example", "path", "/.well-known/webmonitoring-verification.txt"},
		{domain.VerificationDNS, "https://dns.example", "recordType", "TXT"},
		{domain.VerificationMeta, "https://meta.example", "tag", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.method), func(t *testing.T) {
			asset := domain.Asset{
				ID: "asset-" + string(tc.method), TenantID: defaultTenant, Name: "a",
				URL: tc.url, Type: domain.AssetOfficial, OwnerID: admin.ID,
				VerificationStatus: domain.VerificationPending, VerificationMethod: tc.method,
			}
			if err := repository.CreateAsset(context.Background(), asset); err != nil {
				t.Fatal(err)
			}
			response := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"ISSUE"}`)
			if response.Code != http.StatusOK {
				t.Fatalf("issue status=%d body=%s", response.Code, response.Body.String())
			}
			var instructions map[string]string
			if err := json.NewDecoder(response.Body).Decode(&instructions); err != nil {
				t.Fatal(err)
			}
			if instructions["method"] != string(tc.method) {
				t.Fatalf("method=%q want %q", instructions["method"], tc.method)
			}
			if instructions["content"] == "" {
				t.Fatal("expected a non-empty content token")
			}
			if tc.expectValue != "" && instructions[tc.expectKey] != tc.expectValue {
				t.Fatalf("%s=%q want %q", tc.expectKey, instructions[tc.expectKey], tc.expectValue)
			}
			if tc.expectKey == "tag" && instructions["tag"] == "" {
				t.Fatal("expected a meta tag for META")
			}
		})
	}
}

func TestDNSVerificationFlow(t *testing.T) {
	resolver := fakeResolver{txt: map[string][]string{}}
	repository, server, admin := verificationServer(t, resolver)
	cookie, csrf := loginForTest(t, server, admin.Username)

	asset := domain.Asset{
		ID: "dns-asset", TenantID: defaultTenant, Name: "dns",
		URL: "https://verify.example", Type: domain.AssetOfficial, OwnerID: admin.ID,
		VerificationStatus: domain.VerificationPending, VerificationMethod: domain.VerificationDNS,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}

	issue := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"ISSUE"}`)
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var instructions map[string]string
	if err := json.NewDecoder(issue.Body).Decode(&instructions); err != nil {
		t.Fatal(err)
	}
	if instructions["record"] != "_webmonitoring-verify.verify.example" {
		t.Fatalf("record=%q", instructions["record"])
	}

	// Wrong TXT value must fail verification.
	resolver.txt["_webmonitoring-verify.verify.example"] = []string{"wrong-value"}
	verify := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"VERIFY"}`)
	if verify.Code != http.StatusUnprocessableEntity {
		t.Fatalf("verify wrong token status=%d body=%s", verify.Code, verify.Body.String())
	}

	// Correct TXT value must succeed and mark the asset verified + enabled.
	resolver.txt["_webmonitoring-verify.verify.example"] = []string{instructions["content"]}
	verify = doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"VERIFY"}`)
	if verify.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", verify.Code, verify.Body.String())
	}
	var verified domain.Asset
	if err := json.NewDecoder(verify.Body).Decode(&verified); err != nil {
		t.Fatal(err)
	}
	if verified.VerificationStatus != domain.VerificationVerified || !verified.Enabled {
		t.Fatalf("unexpected verified asset: %#v", verified)
	}
}

func TestMetaVerificationFlow(t *testing.T) {
	repository, server, admin := verificationServer(t, nil)
	cookie, csrf := loginForTest(t, server, admin.Username)

	var served string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><meta name="webmonitoring-verification" content="%s"></head><body></body></html>`, served)
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	server.guard.AllowPrivate = true
	server.guard.AllowedPorts[targetURL.Port()] = struct{}{}

	asset := domain.Asset{
		ID: "meta-asset", TenantID: defaultTenant, Name: "meta",
		URL: target.URL, Type: domain.AssetOfficial, OwnerID: admin.ID,
		VerificationStatus: domain.VerificationPending, VerificationMethod: domain.VerificationMeta,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}

	issue := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"ISSUE"}`)
	if issue.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issue.Code, issue.Body.String())
	}
	var instructions map[string]string
	if err := json.NewDecoder(issue.Body).Decode(&instructions); err != nil {
		t.Fatal(err)
	}

	// Publish the token as a meta tag and verify.
	served = instructions["content"]
	verify := doJSON(t, server, http.MethodPost, "/api/v1/assets/"+asset.ID+"/verification", cookie, csrf, `{"action":"VERIFY"}`)
	if verify.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", verify.Code, verify.Body.String())
	}
	var verified domain.Asset
	if err := json.NewDecoder(verify.Body).Decode(&verified); err != nil {
		t.Fatal(err)
	}
	if verified.VerificationStatus != domain.VerificationVerified || !verified.Enabled {
		t.Fatalf("unexpected verified asset: %#v", verified)
	}
}
