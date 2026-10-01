package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

// TestListAssetsKeysetPagination seeds 120 assets and pages through them with a
// limit of 50, asserting the pages are non-overlapping, complete, and ordered
// by (created_at, id) descending.
func TestListAssetsKeysetPagination(t *testing.T) {
	repository := NewMemoryRepository()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 120; i++ {
		asset := domain.Asset{
			ID:        fmt.Sprintf("asset-%03d", i),
			TenantID:  "tenant",
			Name:      fmt.Sprintf("asset %d", i),
			URL:       fmt.Sprintf("https://example-%03d.test", i),
			Type:      domain.AssetOfficial,
			OwnerID:   "owner",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := repository.CreateAsset(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
	}

	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		items, next, err := repository.ListAssets(context.Background(), "tenant", ListOptions{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			seen = append(seen, item.ID)
		}
		if next == "" {
			break
		}
		if page > 4 {
			t.Fatal("pagination did not terminate")
		}
		cursor = next
	}

	if len(seen) != 120 {
		t.Fatalf("got %d assets across pages, want 120", len(seen))
	}
	unique := make(map[string]struct{}, len(seen))
	for _, id := range seen {
		if _, dup := unique[id]; dup {
			t.Fatalf("duplicate asset id %q across pages", id)
		}
		unique[id] = struct{}{}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] < seen[i] {
			t.Fatalf("order not descending at %d: %q before %q", i, seen[i-1], seen[i])
		}
	}
}
