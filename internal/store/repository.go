package store

import (
	"context"
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

	ListTenants(ctx context.Context, opts ListOptions) ([]domain.Tenant, string, error)
	GetTenant(ctx context.Context, tenantID string) (domain.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (domain.Tenant, error)
	CreateTenant(ctx context.Context, tenant domain.Tenant) error
	UpdateTenant(ctx context.Context, tenant domain.Tenant) error
	// CountTenantResources reports how many tenant-scoped rows exist per
	// resource kind, so a destructive tenant delete can fail closed when the
	// tenant is not empty. Missing kinds are reported as zero.
	CountTenantResources(ctx context.Context, tenantID string) (map[string]int, error)
	// DeleteTenant removes a tenant and all of its tenant-scoped rows.
	DeleteTenant(ctx context.Context, tenantID string) error

	CreateUser(ctx context.Context, user domain.User) error
	FindUserByUsername(ctx context.Context, tenantID, username string) (domain.User, error)
	GetUser(ctx context.Context, tenantID, id string) (domain.User, error)
	ListUsers(ctx context.Context, tenantID string) ([]domain.User, error)
	UpdateUser(ctx context.Context, user domain.User) error

	CreateAsset(ctx context.Context, asset domain.Asset) error
	UpdateAsset(ctx context.Context, asset domain.Asset) error
	GetAsset(ctx context.Context, tenantID, id string) (domain.Asset, error)
	ListAssets(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Asset, string, error)
	// CountAssets returns the tenant's asset total and how many of them are
	// verified, as one aggregate query, so dashboards never page the whole
	// registry into memory.
	CountAssets(ctx context.Context, tenantID string) (total int, verified int, err error)

	CreateSite(ctx context.Context, site domain.Site) error
	UpdateSite(ctx context.Context, site domain.Site) error
	GetSite(ctx context.Context, tenantID, id string) (domain.Site, error)
	ListSites(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Site, string, error)
	// DeleteSite removes one site. It reports ErrConflict when any asset still
	// references the site: site_id is an optional grouping on the asset, so
	// cascading the delete would silently unfile live assets, and the database
	// foreign key would refuse the write anyway. The caller unassigns first.
	DeleteSite(ctx context.Context, tenantID, id string) error
	// CountAssetsBySite reports how many assets in the tenant reference the
	// site, so the handler can name the count in its conflict response.
	CountAssetsBySite(ctx context.Context, tenantID, siteID string) (int, error)

	UpsertPolicy(ctx context.Context, policy domain.MonitorPolicy) error
	// RewrapPolicy rewrites one policy's stored login credentials under the
	// current key. Like RewrapAlertChannel it is the narrow rotation write, not a
	// general update: the caller reads the policy first and changes only the
	// ciphertext fields.
	RewrapPolicy(ctx context.Context, policy domain.MonitorPolicy) error
	GetPolicy(ctx context.Context, tenantID, assetID string) (domain.MonitorPolicy, error)
	ListPolicies(ctx context.Context, tenantID string, opts ListOptions) ([]domain.MonitorPolicy, string, error)

	CreateBaseline(ctx context.Context, baseline domain.BaselineVersion) error
	UpdateBaseline(ctx context.Context, baseline domain.BaselineVersion) error
	GetBaseline(ctx context.Context, tenantID, id string) (domain.BaselineVersion, error)
	GetApprovedBaseline(ctx context.Context, tenantID, assetID string) (domain.BaselineVersion, error)
	ListBaselines(ctx context.Context, tenantID, assetID string, opts ListOptions) ([]domain.BaselineVersion, string, error)

	CreateJob(ctx context.Context, job domain.DetectionJob) error
	ClaimJob(ctx context.Context, tenantID, id string, startedAt time.Time) (bool, error)
	UpdateJob(ctx context.Context, job domain.DetectionJob) error
	GetJob(ctx context.Context, tenantID, id string) (domain.DetectionJob, error)
	ListJobs(ctx context.Context, tenantID string, opts ListOptions) ([]domain.DetectionJob, string, error)
	CancelJob(ctx context.Context, tenantID, id string) (bool, error)
	ReapTimedOut(ctx context.Context, tenantID string, before time.Time) (int, error)
	// JobSchedulingState summarizes detection_jobs per (asset, mode) so the
	// scheduler can decide what is due without hauling every historical job
	// row into memory. LatestCreatedAt is zero when the pair has no rows at
	// left at all; HasActive reports whether a QUEUED or RUNNING job exists,
	// which is what makes a pair ineligible regardless of age.
	JobSchedulingState(ctx context.Context, tenantID string) ([]JobActivity, error)
	// DeleteJobsBefore removes terminal detection jobs older than the cutoff.
	// QUEUED and RUNNING rows are never touched: a stale RUNNING row is crash
	// recovery state for ClaimJob, and a QUEUED row is work not yet started.
	DeleteJobsBefore(ctx context.Context, tenantID string, before time.Time) (int, error)

	CreateEvidence(ctx context.Context, evidence domain.Evidence) error
	GetEvidence(ctx context.Context, tenantID, id string) (domain.Evidence, error)
	ListEvidence(ctx context.Context, tenantID, assetID string, opts ListOptions) ([]domain.Evidence, string, error)
	ListExpiredEvidence(ctx context.Context, tenantID string, before time.Time) ([]domain.Evidence, error)
	IsEvidenceReferenced(ctx context.Context, tenantID, id string) (bool, error)
	DeleteEvidence(ctx context.Context, tenantID, id string) error

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
	UpsertIncident(ctx context.Context, incident domain.Incident) (domain.Incident, IncidentUpsertOutcome, error)
	UpdateIncident(ctx context.Context, incident domain.Incident) error
	GetIncident(ctx context.Context, tenantID, id string) (domain.Incident, error)
	ListIncidents(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Incident, string, error)
	// IncidentStatusCounts returns, in one aggregate query, how many incidents
	// are open, how many of those are high or critical, and how many of those
	// are critical. Dashboard and metrics read the same numbers without
	// depending on a page of the incident list.
	IncidentStatusCounts(ctx context.Context, tenantID string) (open int, highRisk int, critical int, err error)
	// CountOpenIncidents is the platform-wide form of IncidentStatusCounts for
	// /metrics: a scrape carries no tenant context, so the gauges must count
	// across every tenant.
	CountOpenIncidents(ctx context.Context) (open int, highRisk int, critical int, err error)
	// FailedJobCount returns how many detection jobs ended FAILED or TIMED_OUT,
	// so dashboards and reports never page the (retention-bounded but still
	// large) job table into memory just to count it.
	FailedJobCount(ctx context.Context, tenantID string) (int, error)
	// AppendIncidentEvent adds one entry to an incident's timeline. Entries are
	// append-only by design: the incident row cannot hold its own history,
	// because the upsert overwrites `signals` on every recurrence.
	AppendIncidentEvent(ctx context.Context, event domain.IncidentEvent) error
	// ListIncidentEvents returns one incident's timeline, newest first.
	ListIncidentEvents(ctx context.Context, tenantID, incidentID string, opts ListOptions) ([]domain.IncidentEvent, string, error)

	CreateAlertChannel(ctx context.Context, channel domain.AlertChannel) error
	// GetAlertChannel returns one channel with its secret decrypted, scoped to
	// the tenant. A cross-tenant id is ErrNotFound rather than a distinguishable
	// error, matching every other single-resource read.
	GetAlertChannel(ctx context.Context, tenantID, id string) (domain.AlertChannel, error)
	// UpdateAlertChannel rewrites an existing row in place. It is deliberately
	// not delete-then-insert: the encrypted columns are bound to the row's id
	// and tenant through their associated data, so a new id would leave the
	// ciphertext permanently undecryptable.
	UpdateAlertChannel(ctx context.Context, channel domain.AlertChannel) error
	// DeleteAlertChannel removes one channel, scoped to the tenant. Deliveries
	// already recorded against it are kept: they are the record that an alert
	// was sent, and deleting the channel must not erase that.
	DeleteAlertChannel(ctx context.Context, tenantID, id string) error
	// RewrapAlertChannel rewrites only the two encrypted columns of one channel.
	// It is the key-rotation write: unlike UpdateAlertChannel it cannot touch a
	// name or a recipient list, so a rotation pass has no way to alter a row's
	// meaning while re-sealing it.
	//
	// It stores the values it is given, which are already re-wrapped ciphertext
	// from the pass's own keyring -- the same contract as RewrapPolicy. It must
	// not seal them again: doing so left the row holding ciphertext and made the
	// next read hand the ciphertext to the dispatcher as if it were the URL.
	RewrapAlertChannel(ctx context.Context, channel domain.AlertChannel) error
	ListAlertChannels(ctx context.Context, tenantID string) ([]domain.AlertChannel, error)
	// ListAlertChannelsWithSecrets is the dispatcher's read: the URL and secret
	// are decrypted, because sending an alert needs both in the clear.
	ListAlertChannelsWithSecrets(ctx context.Context, tenantID string) ([]domain.AlertChannel, error)
	// ListAlertChannelsEncrypted returns the two encrypted columns exactly as
	// stored -- version-prefixed ciphertext, not plaintext.
	//
	// It exists for the key-rotation pass, and the distinction is load-bearing:
	// re-sealing a value that has already been decrypted sees no version prefix,
	// treats it as plaintext from before encryption, and reports the row as
	// already current. A rotation that read through the decrypting accessor
	// therefore rewrapped every policy and no channel at all, and the deployment
	// only found out when the retired key was dropped and alert delivery failed.
	ListAlertChannelsEncrypted(ctx context.Context, tenantID string) ([]domain.AlertChannel, error)
	CreateAlertDelivery(ctx context.Context, delivery domain.AlertDelivery) error
	UpdateAlertDelivery(ctx context.Context, delivery domain.AlertDelivery) error
	ListAlertDeliveries(ctx context.Context, tenantID, incidentID string, opts ListOptions) ([]domain.AlertDelivery, string, error)

	AppendAudit(ctx context.Context, event domain.AuditEvent) error
	ListAudits(ctx context.Context, tenantID string, filter AuditFilter, opts ListOptions) ([]domain.AuditEvent, string, error)
	DeleteAuditsBefore(ctx context.Context, tenantID string, before time.Time) (int, error)
}

// JobActivity is the scheduler's per-(asset, mode) view of the job table: the
// newest job's creation time and whether an active job currently occupies the
// slot. It exists so scheduling reads stay flat as history accumulates.
type JobActivity struct {
	AssetID         string
	Mode            string
	LatestCreatedAt time.Time
	HasActive       bool
}
