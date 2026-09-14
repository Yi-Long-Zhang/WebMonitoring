package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

type LocalStore struct {
	root string
}

func (s *LocalStore) Ready(context.Context) error {
	return nil
}

func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, errors.New("evidence root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence root: %w", err)
	}
	return &LocalStore{root: absolute}, nil
}

func (s *LocalStore) Put(tenantID, assetID, jobID, kind, mediaType string, content []byte) (domain.Evidence, error) {
	id := identity.New()
	now := time.Now().UTC()
	objectKey := filepath.Join(tenantID, assetID, now.Format("2006/01/02"), id+"-"+safeKind(kind))
	target := filepath.Join(s.root, objectKey)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return domain.Evidence{}, fmt.Errorf("create evidence directory: %w", err)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("create immutable evidence: %w", err)
	}
	hash := sha256.New()
	size, writeErr := io.Copy(io.MultiWriter(file, hash), bytes.NewReader(content))
	closeErr := file.Close()
	if writeErr != nil {
		return domain.Evidence{}, fmt.Errorf("write evidence: %w", writeErr)
	}
	if closeErr != nil {
		return domain.Evidence{}, fmt.Errorf("close evidence: %w", closeErr)
	}
	return domain.Evidence{
		ID:        id,
		TenantID:  tenantID,
		AssetID:   assetID,
		JobID:     jobID,
		Kind:      kind,
		MediaType: mediaType,
		Size:      size,
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
		ObjectKey: objectKey,
		CreatedAt: now,
	}, nil
}

func (s *LocalStore) Open(objectKey string) (io.ReadCloser, error) {
	cleaned := filepath.Clean(objectKey)
	target := filepath.Join(s.root, cleaned)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return nil, errors.New("invalid evidence object key")
	}
	return os.Open(target)
}

func (s *LocalStore) Delete(objectKey string) error {
	cleaned := filepath.Clean(objectKey)
	target := filepath.Join(s.root, cleaned)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return errors.New("invalid evidence object key")
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var _ Store = (*LocalStore)(nil)

func safeKind(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "evidence.bin"
	}
	return builder.String()
}
