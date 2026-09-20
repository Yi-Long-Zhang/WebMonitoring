package store

import (
	"errors"
	"time"

	"webmonitoring/internal/domain"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource conflicts with existing state")
	// ErrJobAlreadyActive reports that a QUEUED or RUNNING detection job
	// already exists for the same tenant, asset and mode. Callers treat it as
	// an idempotent no-op rather than a failure: the scheduler decides a check
	// is due by reading the job table, so concurrent control-plane replicas can
	// reach the same decision for the same asset and both try to queue it.
	ErrJobAlreadyActive = errors.New("detection job already queued or running")
)

// JobLease is how long a detection job may run before its RUNNING claim is
// considered stale. ClaimJob reclaims stale RUNNING jobs and ReapTimedOut
// moves them to TIMED_OUT, so a worker crash cannot leave a job RUNNING forever.
const JobLease = 5 * time.Minute

type Repository interface {
	ListTenants(opts ListOptions) ([]domain.Tenant, string, error)
	GetTenant(tenantID string) (domain.Tenant, error)
	GetTenantBySlug(slug string) (domain.Tenant, error)
	CreateTenant(domain.Tenant) error
	UpdateTenant(domain.Tenant) error
	// CountTenantResources reports how many tenant-scoped rows exist per
	// resource kind, so a destructive tenant delete can fail closed when the
	// tenant is not empty. Missing kinds are reported as zero.
	CountTenantResources(tenantID string) (map[string]int, error)
	// DeleteTenant removes a tenant and all of its tenant-scoped rows.
	DeleteTenant(tenantID string) error

	CreateUser(domain.User) error
	FindUserByUsername(tenantID, username string) (domain.User, error)
	GetUser(tenantID, id string) (domain.User, error)
	ListUsers(tenantID string) ([]domain.User, error)
	UpdateUser(domain.User) error

	CreateAsset(domain.Asset) error
	UpdateAsset(domain.Asset) error
	GetAsset(tenantID, id string) (domain.Asset, error)
	ListAssets(tenantID string, opts ListOptions) ([]domain.Asset, string, error)

	CreateSite(domain.Site) error
	UpdateSite(domain.Site) error
	GetSite(tenantID, id string) (domain.Site, error)
	ListSites(tenantID string, opts ListOptions) ([]domain.Site, string, error)

	UpsertPolicy(domain.MonitorPolicy) error
	GetPolicy(tenantID, assetID string) (domain.MonitorPolicy, error)
	ListPolicies(tenantID string, opts ListOptions) ([]domain.MonitorPolicy, string, error)

	CreateBaseline(domain.BaselineVersion) error
	UpdateBaseline(domain.BaselineVersion) error
	GetBaseline(tenantID, id string) (domain.BaselineVersion, error)
	GetApprovedBaseline(tenantID, assetID string) (domain.BaselineVersion, error)
	ListBaselines(tenantID, assetID string, opts ListOptions) ([]domain.BaselineVersion, string, error)

	CreateJob(domain.DetectionJob) error
	ClaimJob(tenantID, id string, startedAt time.Time) (bool, error)
	UpdateJob(domain.DetectionJob) error
	GetJob(tenantID, id string) (domain.DetectionJob, error)
	ListJobs(tenantID string, opts ListOptions) ([]domain.DetectionJob, string, error)
	CancelJob(tenantID, id string) (bool, error)
	ReapTimedOut(tenantID string, before time.Time) (int, error)

	CreateEvidence(domain.Evidence) error
	GetEvidence(tenantID, id string) (domain.Evidence, error)
	ListEvidence(tenantID, assetID string, opts ListOptions) ([]domain.Evidence, string, error)
	ListExpiredEvidence(tenantID string, before time.Time) ([]domain.Evidence, error)
	IsEvidenceReferenced(tenantID, id string) (bool, error)
	DeleteEvidence(tenantID, id string) error

	UpsertIncident(domain.Incident) (domain.Incident, bool, error)
	UpdateIncident(domain.Incident) error
	GetIncident(tenantID, id string) (domain.Incident, error)
	ListIncidents(tenantID string, opts ListOptions) ([]domain.Incident, string, error)

	CreateAlertChannel(domain.AlertChannel) error
	ListAlertChannels(tenantID string) ([]domain.AlertChannel, error)
	ListAlertChannelsWithSecrets(tenantID string) ([]domain.AlertChannel, error)
	CreateAlertDelivery(domain.AlertDelivery) error
	UpdateAlertDelivery(domain.AlertDelivery) error
	ListAlertDeliveries(tenantID, incidentID string, opts ListOptions) ([]domain.AlertDelivery, string, error)

	AppendAudit(domain.AuditEvent) error
	ListAudits(tenantID string, opts ListOptions) ([]domain.AuditEvent, string, error)
	DeleteAuditsBefore(tenantID string, before time.Time) (int, error)
}
