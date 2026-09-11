package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3Store(ctx context.Context, endpoint, accessKey, secretKey, bucket string) (*S3Store, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: parsed.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("configure S3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check evidence bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			existsAfterRace, checkErr := client.BucketExists(ctx, bucket)
			if checkErr != nil || !existsAfterRace {
				return nil, fmt.Errorf("create evidence bucket: %w", err)
			}
		}
	}
	return &S3Store{client: client, bucket: bucket}, nil
}

func (s *S3Store) Put(tenantID, assetID, jobID, kind, mediaType string, content []byte) (domain.Evidence, error) {
	id := identity.New()
	now := time.Now().UTC()
	objectKey := path.Join(tenantID, assetID, now.Format("2006/01/02"), id+"-"+safeKind(kind))
	sum := sha256.Sum256(content)
	_, err := s.client.PutObject(
		context.Background(), s.bucket, objectKey, bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: mediaType, SendContentMd5: true},
	)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("write immutable evidence object: %w", err)
	}
	return domain.Evidence{
		ID: id, TenantID: tenantID, AssetID: assetID, JobID: jobID, Kind: kind,
		MediaType: mediaType, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
		ObjectKey: objectKey, CreatedAt: now,
	}, nil
}

func (s *S3Store) Open(objectKey string) (io.ReadCloser, error) {
	cleaned := path.Clean("/" + objectKey)
	if strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("invalid evidence object key")
	}
	object, err := s.client.GetObject(context.Background(), s.bucket, strings.TrimPrefix(cleaned, "/"), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, err
	}
	return object, nil
}

var _ Store = (*S3Store)(nil)
