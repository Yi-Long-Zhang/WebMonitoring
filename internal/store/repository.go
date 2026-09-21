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

// JobLease is the default for how long a detection job may run before its
// RUNNING claim is considered stale. ClaimJob reclaims stale RUNNING jobs and
// ReapTimedOut moves them to TIMED_OUT, so a worker crash cannot leave a job
// RUNNING forever.
//
// It is only a default: an implementation returns its own value from JobLease()
// so a deployment can widen it. The lease has to exceed the slowest possible
// detection, and the scheduler's reap cutoff must equal the claim reclaim
// cutoff exactly, or a peer replica can mark a job TIMED_OUT while it is still
// working. Exposing it here is what keeps the two in step.
const JobLease = 5 * time.Minute

// IncidentUpsertOutcome says what one detection round did to an incident.
//
// RECURRED and REOPENED both merge into an existing row; the difference is the
// signal an operator needs. RECURRED means a problem that was still on the books
// happened again. REOPENED means a problem they had already closed or dismissed
// came back, which is the one that must not be silently folded into a routine
// update count.
type IncidentUpsertOutcome int

const (
	// IncidentCreated is a fingerprint that had no incident at all.
	IncidentCreated IncidentUpsertOutcome = iota
	// IncidentRecurred is a fingerprint that matched a live incident.
	IncidentRecurred
	// IncidentReopened is a fingerprint that matched a CLOSED or FALSE_POSITIVE
	// incident, which is now OPEN again under the same id.
	IncidentReopened
)

type Repository interface {
	// JobLease reports the lease this repository enforces, and SetJobLease
	// replaces the default. The scheduler reads the lease rather than importing
	// the constant, so the reap cutoff cannot drift from the reclaim cutoff.
	JobLease() time.Duration
	SetJobLease(time.Duration)

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
	// DeleteSite removes one site. It reports ErrConflict when any asset still
	// references the site: site_id is an optional grouping on the asset, so
	// cascading the delete would silently unfile live assets, and the database
	// foreign key would refuse the write anyway. The caller unassigns first.
	DeleteSite(tenantID, id string) error
	// CountAssetsBySite reports how many assets in the tenant reference the
	// site, so the handler can name the count in its conflict response.
	CountAssetsBySite(tenantID, siteID string) (int, error)

	UpsertPolicy(domain.MonitorPolicy) error
	// RewrapPolicy rewrites one policy's stored login credentials under the
	// current key. Like RewrapAlertChannel it is the narrow rotation write, not a
	// general update: the caller reads the policy first and changes only the
	// ciphertext fields.
	RewrapPolicy(domain.MonitorPolicy) error
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

	// UpsertIncident merges one detection round into the incident that shares
	// its (tenant, asset, baseline version, fingerprint), or creates it.
	//
	// The outcome is a three-way value rather than a bool because a recurrence
	// has two distinct meanings: landing on a live incident is RECURRED, while
	// landing on one an operator already closed or dismissed is REOPENED -- the
	// thing they closed came back. Both merge rather than insert, so a bool
	// cannot tell them apart, and the alert the operator receives depends on
	// which it was.
	//
	// A recurrence on a terminal incident reopens it: the incident keeps its id
	// and its timeline, and its status returns to OPEN. Until migration 000015
	// this case instead inserted a second incident with the same fingerprint.
	UpsertIncident(domain.Incident) (domain.Incident, IncidentUpsertOutcome, error)
	UpdateIncident(domain.Incident) error
	GetIncident(tenantID, id string) (domain.Incident, error)
	ListIncidents(tenantID string, opts ListOptions) ([]domain.Incident, string, error)
	// AppendIncidentEvent adds one entry to an incident's timeline. Entries are
	// append-only by design: the incident row cannot hold its own history,
	// because the upsert overwrites `signals` on every recurrence.
	AppendIncidentEvent(domain.IncidentEvent) error
	// ListIncidentEvents returns one incident's timeline, newest first.
	ListIncidentEvents(tenantID, incidentID string, opts ListOptions) ([]domain.IncidentEvent, string, error)

	CreateAlertChannel(domain.AlertChannel) error
	// GetAlertChannel returns one channel with its secret decrypted, scoped to
	// the tenant. A cross-tenant id is ErrNotFound rather than a distinguishable
	// error, matching every other single-resource read.
	GetAlertChannel(tenantID, id string) (domain.AlertChannel, error)
	// UpdateAlertChannel rewrites an existing row in place. It is deliberately
	// not delete-then-insert: the encrypted columns are bound to the row's id
	// and tenant through their associated data, so a new id would leave the
	// ciphertext permanently undecryptable.
	UpdateAlertChannel(domain.AlertChannel) error
	// DeleteAlertChannel removes one channel, scoped to the tenant. Deliveries
	// already recorded against it are kept: they are the record that an alert
	// was sent, and deleting the channel must not erase that.
	DeleteAlertChannel(tenantID, id string) error
	// RewrapAlertChannel rewrites only the two encrypted columns of one channel.
	// It is the key-rotation write: unlike UpdateAlertChannel it cannot touch a
	// name or a recipient list, so a rotation pass has no way to alter a row's
	// meaning while re-sealing it.
	RewrapAlertChannel(domain.AlertChannel) error
	ListAlertChannels(tenantID string) ([]domain.AlertChannel, error)
	ListAlertChannelsWithSecrets(tenantID string) ([]domain.AlertChannel, error)
	CreateAlertDelivery(domain.AlertDelivery) error
	UpdateAlertDelivery(domain.AlertDelivery) error
	ListAlertDeliveries(tenantID, incidentID string, opts ListOptions) ([]domain.AlertDelivery, string, error)

	AppendAudit(domain.AuditEvent) error
	ListAudits(tenantID string, filter AuditFilter, opts ListOptions) ([]domain.AuditEvent, string, error)
	DeleteAuditsBefore(tenantID string, before time.Time) (int, error)
}
