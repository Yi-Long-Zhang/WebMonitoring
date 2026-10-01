package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

func TestListAssetsPaginationHandler(t *testing.T) {
	repository := store.NewMemoryRepository()
	admin := retentionUser(t, repository, "admin", domain.RoleAdmin)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		asset := domain.Asset{
			ID:        fmt.Sprintf("asset-%d", i),
			TenantID:  defaultTenant,
			Name:      fmt.Sprintf("asset %d", i),
			URL:       fmt.Sprintf("https://example-%d.test", i),
			Type:      domain.AssetOfficial,
			OwnerID:   admin.ID,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
	}
	evidenceStore, err := evidence.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := retentionServer(repository, evidenceStore)
	cookie, _ := loginForTest(t, server, admin.Username)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/assets?limit=2", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Items      []domain.Asset `json:"items"`
		NextCursor string         `json:"nextCursor"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("limit=2 returned %d items, want 2", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatalf("expected a non-empty nextCursor for 5 assets with limit 2")
	}
}
