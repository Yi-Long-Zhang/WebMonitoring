package evidence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"webmonitoring/internal/domain"
)

const maxVerifiedEvidenceBytes = 10 * 1024 * 1024

// ReadVerified reads an evidence object into bounded memory and verifies the
// immutable metadata before it is returned to an API or report exporter.
func ReadVerified(ctx context.Context, store Store, item domain.Evidence) ([]byte, error) {
	if item.Size < 0 || item.Size > maxVerifiedEvidenceBytes {
		return nil, fmt.Errorf("evidence size is outside the allowed verification limit")
	}
	if len(item.SHA256) != sha256.Size*2 {
		return nil, errors.New("evidence has an invalid SHA-256 metadata value")
	}
	expected, err := hex.DecodeString(item.SHA256)
	if err != nil {
		return nil, errors.New("evidence has an invalid SHA-256 metadata value")
	}
	object, err := store.Open(ctx, item.ObjectKey)
	if err != nil {
		return nil, err
	}
	defer object.Close() //nolint:errcheck // 延迟清理：此处失败已无可补救，调用方也不依赖它

	content, err := io.ReadAll(io.LimitReader(object, item.Size+1))
	if err != nil {
		return nil, fmt.Errorf("read evidence object: %w", err)
	}
	if int64(len(content)) != item.Size {
		return nil, errors.New("evidence size does not match metadata")
	}
	sum := sha256.Sum256(content)
	if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return nil, errors.New("evidence checksum does not match metadata")
	}
	return content, nil
}
