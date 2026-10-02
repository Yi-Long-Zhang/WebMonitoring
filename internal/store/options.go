package store

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/domain"
)

// ListOptions controls pagination of repository list methods. A non-positive
// Limit requests an unbounded result. Internal callers (scheduler, dashboard
// aggregation, incident upsert bookkeeping) pass Limit 0 because the MVP data
// volume makes a full scan acceptable; API handlers always bound the result.
type ListOptions struct {
	Limit  int
	Cursor string
}

// AuditFilter narrows an audit listing. Every field is optional; the zero value
// means "do not filter on this dimension", so AuditFilter{} returns everything
// the tenant has.
//
// These dimensions live in their own type rather than in ListOptions because
// ListOptions is shared by every list method: putting audit predicates there
// would let an unrelated handler pass them and would make the zero value mean
// something different per method.
//
// Filtering is independent of keyset pagination. The cursor is a position in
// the (created_at DESC, id DESC) ordering, so narrowing the filter never
// invalidates a cursor already handed to a client.
type AuditFilter struct {
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	// Inclusive bounds. A zero time means unbounded on that side.
	From time.Time
	To   time.Time
}

// Matches reports whether an event satisfies every set dimension. The Postgres
// implementation mirrors these predicates in SQL; both are exercised by the
// same cross-store test so the two cannot drift silently.
func (f AuditFilter) Matches(event domain.AuditEvent) bool {
	switch {
	case f.ActorID != "" && event.ActorID != f.ActorID,
		f.Action != "" && event.Action != f.Action,
		f.ResourceType != "" && event.ResourceType != f.ResourceType,
		f.ResourceID != "" && event.ResourceID != f.ResourceID,
		f.Result != "" && event.Result != f.Result,
		!f.From.IsZero() && event.CreatedAt.Before(f.From),
		!f.To.IsZero() && event.CreatedAt.After(f.To):
		return false
	}
	return true
}

// IncidentFilter narrows an incident listing. Like AuditFilter, every field is
// optional and the zero value means "do not filter on this dimension", so
// IncidentFilter{} returns every incident the tenant has.
//
// It is a separate type from AuditFilter for the same reason AuditFilter is a
// separate type from ListOptions: the dimensions belong to this listing, and
// sharing one struct across listings would let a handler pass a predicate the
// method cannot honour while the zero value quietly meant something else.
//
// The time bounds are on LastSeenAt because that is the listing's ordering key.
// Bounding on a column the page is not ordered by would let a cursor skip rows
// that a later page should have shown.
type IncidentFilter struct {
	Status   string
	Severity string
	AssetID  string
	OwnerID  string
	// Keyword matches anywhere in the title, case-insensitively.
	Keyword string
	// Inclusive bounds on last_seen_at. A zero time means unbounded.
	From time.Time
	To   time.Time
}

// Matches reports whether an incident satisfies every set dimension. The
// Postgres implementation mirrors these predicates in SQL; both are exercised by
// the same cross-store test so the two cannot drift silently.
func (f IncidentFilter) Matches(incident domain.Incident) bool {
	switch {
	case f.Status != "" && string(incident.Status) != f.Status,
		f.Severity != "" && string(incident.Severity) != f.Severity,
		f.AssetID != "" && incident.AssetID != f.AssetID,
		f.OwnerID != "" && incident.OwnerID != f.OwnerID,
		!f.From.IsZero() && incident.LastSeenAt.Before(f.From),
		!f.To.IsZero() && incident.LastSeenAt.After(f.To):
		return false
	}
	if f.Keyword != "" && !strings.Contains(strings.ToLower(incident.Title), strings.ToLower(f.Keyword)) {
		return false
	}
	return true
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
