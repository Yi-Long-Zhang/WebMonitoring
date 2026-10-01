package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
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

// s3Timeouts bounds the phases of one object-storage request that the HTTP
// transport controls: the connect and TLS handshake, and how long the store may
// take to start sending response headers. A wedged or unreachable endpoint then
// fails each request instead of holding an API handler open indefinitely.
//
// There is deliberately no whole-request deadline here. Open hands back a
// streaming reader whose lifetime belongs to the caller, so a single timeout
// covering the entire request would truncate a large download rather than bound
// it; the phases above still cover the call that can block before that reader
// exists.
type s3Timeouts struct {
	dial           time.Duration
	responseHeader time.Duration
}

func defaultS3Timeouts() s3Timeouts {
	return s3Timeouts{dial: 10 * time.Second, responseHeader: 30 * time.Second}
}

func (t s3Timeouts) transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: t.dial, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: t.responseHeader,
	}
}

// s3CompleteRequestTimeout bounds an operation that finishes inside its own
// call: Put already holds the whole body in memory and Delete carries none, so
// the round trip is knowable and a deadline cannot cut a transfer short.
const s3CompleteRequestTimeout = 2 * time.Minute

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
	// timeouts and maxRetries are zero in production, where they mean the
	// built-in bounds and minio-go's own retry default. They are fields so a test
	// can exercise the timeout path against an endpoint that never responds
	// without waiting out either the real deadlines or the retry ladder.
	timeouts   s3Timeouts
	maxRetries int
}

func NewS3Store(ctx context.Context, opts S3Options) (*S3Store, error) {
	parsed, err := url.Parse(opts.Endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	timeouts := opts.timeouts
	if timeouts == (s3Timeouts{}) {
		timeouts = defaultS3Timeouts()
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:      credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure:     parsed.Scheme == "https",
		Region:     opts.Region,
		Transport:  timeouts.transport(),
		MaxRetries: opts.maxRetries,
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
	ctx, cancel := context.WithTimeout(context.Background(), s3CompleteRequestTimeout)
	defer cancel()
	_, err := s.client.PutObject(
		ctx, s.bucket, objectKey, bytes.NewReader(content), int64(len(content)),
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

func (s *S3Store) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	cleaned := path.Clean("/" + objectKey)
	if strings.Contains(cleaned, "..") {
		return nil, fmt.Errorf("invalid evidence object key")
	}
	// The caller's ctx governs the object for as long as it reads the stream, so
	// Open takes it instead of pinning Background here: a client that goes away
	// mid-download cancels the transfer instead of holding the stream open. The
	// transport's response-header timeout is what keeps the Stat below -- the
	// call that would otherwise block on an unreachable store -- from hanging.
	object, err := s.client.GetObject(ctx, s.bucket, strings.TrimPrefix(cleaned, "/"), minio.GetObjectOptions{})
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
	ctx, cancel := context.WithTimeout(context.Background(), s3CompleteRequestTimeout)
	defer cancel()
	return s.client.RemoveObject(ctx, s.bucket, strings.TrimPrefix(cleaned, "/"), minio.RemoveObjectOptions{})
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
