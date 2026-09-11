package security

import (
	"context"
	"net"
	"testing"
)

type staticResolver struct {
	addresses map[string][]net.IP
}

func (r staticResolver) LookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	return r.addresses[host], nil
}

func TestURLGuardRejectsPrivateAndUnsafeTargets(t *testing.T) {
	guard := NewURLGuard(false)
	guard.Resolver = staticResolver{addresses: map[string][]net.IP{
		"public.example":  {net.ParseIP("93.184.216.34")},
		"private.example": {net.ParseIP("10.10.0.4")},
	}}

	if _, _, err := guard.Validate(context.Background(), "https://private.example"); err == nil {
		t.Fatal("expected private target to be rejected")
	}
	if _, _, err := guard.Validate(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("expected non-http scheme to be rejected")
	}
	if _, _, err := guard.Validate(context.Background(), "https://public.example:8443"); err == nil {
		t.Fatal("expected non-standard port to be rejected")
	}
	if _, _, err := guard.Validate(context.Background(), "https://public.example"); err != nil {
		t.Fatalf("expected public target to be accepted: %v", err)
	}
}
