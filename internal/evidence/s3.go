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

func (s *S3Store) Ready(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}

// S3Options configures the object-storage evidence store. It is a struct rather
// than a parameter list because the list had already reached four positional
// strings and the next one would have been easy to transpose.
type S3Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	// Region pins the bucket's region. Leaving it empty makes minio-go discover
	// the region with GetBucketLocation, which costs an extra round trip on every
	// client construction and requires s3:GetBucketLocation in a least-privilege
	// policy. Against a non-AWS S3-compatible endpoint it is usually unnecessary.
	Region string
}

func NewS3Store(ctx context.Context, opts S3Options) (*S3Store, error) {
	parsed, err := url.Parse(opts.Endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: parsed.Scheme == "https",
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("configure S3 client: %w", err)
	}
	bucket := opts.Bucket
	// A bucket that already exists is used as-is. Creating one is attempted only
	// when it is absent, and a policy without s3:CreateBucket fails here at
	// startup rather than later at write time -- the race re-check exists because
	// two replicas can both find the bucket missing and both try to create it.
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check evidence bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: opts.Region}); err != nil {
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

func (s *S3Store) Delete(objectKey string) error {
	cleaned := path.Clean("/" + objectKey)
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("invalid evidence object key")
	}
	return s.client.RemoveObject(context.Background(), s.bucket, strings.TrimPrefix(cleaned, "/"), minio.RemoveObjectOptions{})
}

// PresignedGetURL returns a short-lived URL that fetches the object directly
// from S3 without going through the control plane.
func (s *S3Store) PresignedGetURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	cleaned := path.Clean("/" + objectKey)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("invalid evidence object key")
	}
	presigned, err := s.client.PresignedGetObject(ctx, s.bucket, strings.TrimPrefix(cleaned, "/"), ttl, nil)
	if err != nil {
		return "", err
	}
	return presigned.String(), nil
}

var _ Store = (*S3Store)(nil)
var _ URLPresigner = (*S3Store)(nil)
