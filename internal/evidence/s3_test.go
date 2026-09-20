package evidence

import (
	"context"
	"strings"
	"testing"
)

// A malformed endpoint must fail before any network call, so a typo in
// S3_ENDPOINT is a clear startup error rather than a hang or a confusing
// timeout from the client. This is the only part of NewS3Store reachable
// without an object store, which is why it is the part pinned here; the
// create-bucket and presigned-URL paths need a real endpoint and are covered by
// the object-storage rehearsal instead.
func TestNewS3StoreRejectsMalformedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "not-a-url", "http://", "://missing-scheme", "minio:9000"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := NewS3Store(context.Background(), S3Options{
				Endpoint: endpoint, AccessKey: "key", SecretKey: "secret", Bucket: "evidence",
			})
			if err == nil {
				t.Fatal("a malformed endpoint was accepted")
			}
			if !strings.Contains(err.Error(), "invalid S3 endpoint") {
				t.Fatalf("error does not name the cause: %v", err)
			}
		})
	}
}
