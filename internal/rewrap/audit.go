package rewrap

import (
	"context"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// Action is the audit action recorded for a completed pass. It is a distinct
// action rather than a generic update because a key rotation is exactly the kind
// of event an audit log exists to answer questions about, and "how many values
// moved" has to be legible to someone reading the trail afterwards.
const Action = "security.rewrap"

// AuditWriter is the slice of the repository the audit record needs.
type AuditWriter interface {
	AppendAudit(domain.AuditEvent) error
}

// RecordAudit appends one audit event describing the pass.
//
// It records counts and the primary key version, and nothing that could
// reconstruct a credential: no plaintext, no ciphertext, no associated data.
// The tenant is the platform-wide sentinel rather than any real tenant, because
// a rotation spans the whole installation and attributing it to one customer
// would misplace the record. ActorID stays empty for the same reason it does in
// the scheduled retention pass: no human authorized this specific run.
//
// A dry run is recorded too, and labelled as one. A rotation is planned by
// reading a dry-run report first, so that report is part of the trail -- and a
// row that says only "SUCCESS" would be indistinguishable from a real pass.
func RecordAudit(
	ctx context.Context,
	target AuditWriter,
	primaryVersion int,
	dryRun bool,
	stats Stats,
	passErr error,
) error {
	result := "SUCCESS"
	switch {
	case passErr != nil:
		result = "FAILED"
	case len(stats.Failures) > 0:
		result = "PARTIAL"
	}
	return target.AppendAudit(domain.AuditEvent{
		ID:           identity.New(),
		TenantID:     domain.DefaultTenantID,
		Action:       Action,
		ResourceType: "security.rewrap",
		ResourceID:   Action,
		Result:       result,
		Details: map[string]any{
			"dryRun":            dryRun,
			"primaryVersion":    primaryVersion,
			"channelsScanned":   stats.ChannelsScanned,
			"channelsRewrapped": stats.ChannelsRewrapped,
			"policiesScanned":   stats.PoliciesScanned,
			"policiesRewrapped": stats.PoliciesRewrapped,
			"alreadyCurrent":    stats.AlreadyCurrent,
			"failureCount":      len(stats.Failures),
		},
		CreatedAt: time.Now().UTC(),
	})
}
