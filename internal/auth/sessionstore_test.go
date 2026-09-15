package auth

import (
	"context"
	"testing"
	"time"
)

func TestMemorySessionStoreRoundtrip(t *testing.T) {
	store := newMemorySessionStore()
	ctx := context.Background()
	record := sessionRecord{ID: "s1", CSRFToken: "c", UserID: "u", TenantID: "t", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get(ctx, "s1")
	if !ok || got.ID != "s1" || got.CSRFToken != "c" || got.UserID != "u" {
		t.Fatalf("get mismatch: %+v ok=%v", got, ok)
	}
	if err := store.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(ctx, "s1"); ok {
		t.Fatal("expected gone after delete")
	}
}

func TestMemorySessionStoreMissing(t *testing.T) {
	store := newMemorySessionStore()
	if _, ok := store.Get(context.Background(), "nope"); ok {
		t.Fatal("expected miss for unknown id")
	}
}
