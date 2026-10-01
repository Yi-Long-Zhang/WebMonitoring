package auth

import (
	"context"
	"errors"
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
	got, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.CSRFToken != "c" || got.UserID != "u" {
		t.Fatalf("get mismatch: %+v", got)
	}
	if err := store.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "s1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestMemorySessionStoreMissing(t *testing.T) {
	store := newMemorySessionStore()
	if _, err := store.Get(context.Background(), "nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound for unknown id, got %v", err)
	}
}
