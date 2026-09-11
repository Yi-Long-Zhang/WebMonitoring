package evidence

import (
	"io"

	"webmonitoring/internal/domain"
)

type Store interface {
	Put(tenantID, assetID, jobID, kind, mediaType string, content []byte) (domain.Evidence, error)
	Open(objectKey string) (io.ReadCloser, error)
}
