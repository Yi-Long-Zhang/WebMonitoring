package store

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ListOptions controls pagination of repository list methods. A non-positive
// Limit requests an unbounded result. Internal callers (scheduler, dashboard
// aggregation, incident upsert bookkeeping) pass Limit 0 because the MVP data
// volume makes a full scan acceptable; API handlers always bound the result.
type ListOptions struct {
	Limit  int
	Cursor string
}

// applyPage slices a slice that is already sorted by its keyset key in
// descending order into the page selected by opts. It returns the trimmed page
// plus the cursor for the following page (empty when there is no further page).
// The key function yields the sort tuple (time, id) for an item.
func applyPage[T any](items []T, opts ListOptions, key func(T) (time.Time, string)) ([]T, string) {
	if opts.Limit <= 0 {
		return items, ""
	}
	if opts.Cursor != "" {
		if cursorTime, cursorID, ok := decodeCursor(opts.Cursor); ok {
			start := 0
			for start < len(items) {
				t, id := key(items[start])
				if t.Before(cursorTime) || (t.Equal(cursorTime) && id < cursorID) {
					break
				}
				start++
			}
			items = items[start:]
		}
	}
	if len(items) > opts.Limit {
		t, id := key(items[opts.Limit-1])
		return items[:opts.Limit], encodeCursor(t, id)
	}
	return items, ""
}

// trimPage returns the first Limit items and, when a further item exists, the
// cursor encoded from the last returned item. Callers must already have applied
// any cursor filter (e.g. in SQL). With a non-positive Limit it is a no-op.
func trimPage[T any](items []T, opts ListOptions, key func(T) (time.Time, string)) ([]T, string) {
	if opts.Limit <= 0 || len(items) <= opts.Limit {
		return items, ""
	}
	t, id := key(items[opts.Limit-1])
	return items[:opts.Limit], encodeCursor(t, id)
}

func encodeCursor(t time.Time, id string) string {
	payload := fmt.Sprintf("c1|%d|%s", t.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeCursor(cursor string) (time.Time, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 || parts[0] != "c1" {
		return time.Time{}, "", false
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(0, nanos).UTC(), parts[2], true
}
