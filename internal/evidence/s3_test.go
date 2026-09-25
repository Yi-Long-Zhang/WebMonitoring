package evidence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// A store that accepts the connection and then never sends response headers must
// not hold a caller open. Put and Delete carry their own deadline because the
// whole body is already in memory, but Open cannot: it hands back a streaming
// reader whose lifetime belongs to the caller, so the transport's response-header
// timeout is the only bound covering the Stat that precedes that reader. Each of
// the three has to fail on its own, which is why they are asserted separately.
func TestObjectStoreRequestsAreBoundedAgainstAnUnresponsiveStore(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Building the store checks the bucket, and that request has to succeed
		// or there is no client to test with. BucketExists is a HEAD against the
		// bucket path.
		if request.Method == http.MethodHead && strings.Trim(request.URL.Path, "/") == "evidence" {
			writer.WriteHeader(http.StatusOK)
			return
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	store, err := NewS3Store(context.Background(), S3Options{
		Endpoint: server.URL, AccessKey: "key", SecretKey: "secret", Bucket: "evidence",
		// Pinned so the client does not first ask for the bucket's location,
		// which would be a second request to fake. Region discovery is not what
		// this test is about.
		Region: "us-east-1",
		// One attempt, so the assertion below measures the deadline rather than
		// the retry ladder minio-go would otherwise walk against a dead endpoint.
		timeouts:   s3Timeouts{dial: 2 * time.Second, responseHeader: 250 * time.Millisecond},
		maxRetries: 1,
	})
	if err != nil {
		t.Fatalf("building the store against a reachable bucket failed: %v", err)
	}

	calls := map[string]func() error{
		"Put": func() error {
			_, putErr := store.Put("tenant", "asset", "job", "HTML", "text/html", []byte("evidence"))
			return putErr
		},
		"Open": func() error {
			_, openErr := store.Open("tenant/asset/2026/09/26/object-html")
			return openErr
		},
		"Delete": func() error {
			return store.Delete("tenant/asset/2026/09/26/object-html")
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			if err := call(); err == nil {
				t.Fatalf("%s against a store that never responds succeeded", name)
			}
			// Generous next to the 250ms phase timeout: this asserts the call is
			// bounded at all, not that it gives up the instant the timeout fires.
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("%s waited %s; its deadline did not bound the request", name, elapsed)
			}
		})
	}
}
