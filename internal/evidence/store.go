package evidence

import (
	"context"
	"io"
	"time"

	"webmonitoring/internal/domain"
)

type Store interface {
	Put(tenantID, assetID, jobID, kind, mediaType string, content []byte) (domain.Evidence, error)
	// Open streams one object. The ctx governs the transfer for as long as
	// the caller reads the returned stream, so it must outlive the read.
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	Delete(objectKey string) error
}

// URLPresigner is implemented by stores whose objects can be fetched directly
// with a short-lived external URL (S3). The local store does not implement it;
// instead the control plane serves signed URLs itself.
type URLPresigner interface {
	PresignedGetURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error)
}

type HealthChecker interface {
	Ready(context.Context) error
}
