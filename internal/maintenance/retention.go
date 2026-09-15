// Package maintenance runs periodic data-lifecycle work that used to be
// reachable only through an authorized API call. The same logic backs both the
// manual endpoint and the background loop, so an operator can still trigger a
// dry run on demand and see exactly what the scheduled pass would do.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"webmonitoring/internal/evidence"
	"webmonitoring/internal/store"
)

// Result is the per-tenant outcome of one retention pass. Field names are part
// of the JSON contract of the manual maintenance endpoint.
type Result struct {
	DryRun             bool     `json:"dryRun"`
	ExpiredEvidence    int      `json:"expiredEvidence"`
	DeletedEvidence    int      `json:"deletedEvidence"`
	ProtectedEvidence  int      `json:"protectedEvidence"`
	FailedEvidence     int      `json:"failedEvidence"`
	DeletedAuditEvents int      `json:"deletedAuditEvents"`
	FailureReasons     []string `json:"failureReasons"`
}

// Runner applies evidence and audit retention for a tenant.
//
// Two rules are load-bearing and must not be relaxed:
//   - Evidence still referenced by an incident or baseline is never deleted,
//     even past its retention window.
//   - Metadata is deleted only after the stored object is gone, so a partial
//     failure leaves a discoverable record instead of an orphaned object.
type Runner struct {
	repository     store.Repository
	evidence       evidence.Store
	auditRetention time.Duration
	logger         *slog.Logger
}

func NewRunner(
	repository store.Repository,
	evidenceStore evidence.Store,
	auditRetention time.Duration,
	logger *slog.Logger,
) *Runner {
	return &Runner{repository: repository, evidence: evidenceStore, auditRetention: auditRetention, logger: logger}
}

// RunTenant applies retention for one tenant. When execute is false it only
// reports what would be removed and changes nothing.
func (r *Runner) RunTenant(ctx context.Context, tenantID string, execute bool) (Result, error) {
	now := time.Now().UTC()
	expired, err := r.repository.ListExpiredEvidence(tenantID, now)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		DryRun:          !execute,
		ExpiredEvidence: len(expired),
		FailureReasons:  []string{},
	}
	if !execute {
		return result, nil
	}
	for _, item := range expired {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		referenced, refErr := r.repository.IsEvidenceReferenced(tenantID, item.ID)
		if refErr != nil {
			result.FailedEvidence++
			result.FailureReasons = append(result.FailureReasons, "evidence reference check failed")
			continue
		}
		if referenced {
			result.ProtectedEvidence++
			continue
		}
		if deleteErr := r.evidence.Delete(item.ObjectKey); deleteErr != nil {
			result.FailedEvidence++
			result.FailureReasons = append(result.FailureReasons, "evidence object deletion failed")
			r.logger.Warn("retention could not delete evidence object", "tenant_id", tenantID, "evidence_id", item.ID, "error", deleteErr)
			continue
		}
		if deleteErr := r.repository.DeleteEvidence(tenantID, item.ID); deleteErr != nil {
			result.FailedEvidence++
			result.FailureReasons = append(result.FailureReasons, "evidence metadata deletion failed")
			r.logger.Warn("retention could not delete evidence metadata", "tenant_id", tenantID, "evidence_id", item.ID, "error", deleteErr)
			continue
		}
		result.DeletedEvidence++
	}
	deletedAudits, auditErr := r.repository.DeleteAuditsBefore(tenantID, now.Add(-r.auditRetention))
	if auditErr != nil {
		result.FailureReasons = append(result.FailureReasons, "audit deletion failed")
		r.logger.Warn("retention could not delete audit events", "tenant_id", tenantID, "error", auditErr)
	} else {
		result.DeletedAuditEvents = deletedAudits
	}
	return result, nil
}

// RunAllTenants applies retention to every enabled tenant. Returns the
// per-tenant results keyed by tenant ID for logging and assertions.
//
// Disabled tenants are skipped on purpose: disabling a tenant parks it for
// human review, and silently expiring its evidence during that window would
// destroy the material the review depends on.
func (r *Runner) RunAllTenants(ctx context.Context, execute bool) map[string]Result {
	tenants, _, err := r.repository.ListTenants(store.ListOptions{})
	if err != nil {
		r.logger.Error("list tenants for retention", "error", err)
		return nil
	}
	results := make(map[string]Result, len(tenants))
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return results
		}
		if tenant.Disabled {
			r.logger.Info("skipping retention for disabled tenant", "tenant_id", tenant.ID)
			continue
		}
		result, runErr := r.RunTenant(ctx, tenant.ID, execute)
		if runErr != nil {
			r.logger.Error("retention pass failed", "tenant_id", tenant.ID, "error", runErr)
			continue
		}
		results[tenant.ID] = result
		if execute && (result.DeletedEvidence > 0 || result.DeletedAuditEvents > 0 || result.FailedEvidence > 0) {
			r.logger.Info("retention pass complete", "tenant_id", tenant.ID,
				"deleted_evidence", result.DeletedEvidence,
				"protected_evidence", result.ProtectedEvidence,
				"failed_evidence", result.FailedEvidence,
				"deleted_audit_events", result.DeletedAuditEvents,
			)
		}
	}
	return results
}

// RunDaily schedules RunAllTenants once at startup and then every interval until
// ctx is cancelled. The initial run is intentional: an instance that restarts
// more often than the interval would otherwise never clean up.
func (r *Runner) RunDaily(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.RunAllTenants(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunAllTenants(ctx, true)
		}
	}
}
