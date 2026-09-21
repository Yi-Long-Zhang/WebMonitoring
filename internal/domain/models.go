package domain

import "time"

type Role string

const (
	RoleAdmin      Role = "ADMIN"
	RoleOperator   Role = "OPERATOR"
	RoleAuditor    Role = "AUDITOR"
	RoleSuperAdmin Role = "SUPER_ADMIN"
)

// DefaultTenantID is the tenant seeded by the initial migration. It is the
// fallback when a login request does not specify a tenant.
const DefaultTenantID = "00000000-0000-4000-8000-000000000001"

type AssetType string

const (
	AssetOfficial AssetType = "OFFICIAL"
	AssetBusiness AssetType = "BUSINESS"
	AssetLogin    AssetType = "LOGIN"
	AssetOther    AssetType = "OTHER"
)

type VerificationStatus string

const (
	VerificationPending   VerificationStatus = "PENDING"
	VerificationVerified  VerificationStatus = "VERIFIED"
	VerificationRejected  VerificationStatus = "REJECTED"
	VerificationSuspended VerificationStatus = "SUSPENDED"
)

// VerificationMethod selects how ownership of an asset is proven: a well-known
// file (FILE), a DNS TXT record (DNS), or an HTML meta tag (META). The method is
// chosen at asset creation and never changes afterwards.
type VerificationMethod string

const (
	VerificationFile VerificationMethod = "FILE"
	VerificationDNS  VerificationMethod = "DNS"
	VerificationMeta VerificationMethod = "META"
)

type BaselineStatus string

const (
	BaselineCandidate BaselineStatus = "CANDIDATE"
	BaselineApproved  BaselineStatus = "APPROVED"
	BaselineRejected  BaselineStatus = "REJECTED"
	BaselineRetired   BaselineStatus = "RETIRED"
)

type JobStatus string

const (
	JobQueued    JobStatus = "QUEUED"
	JobRunning   JobStatus = "RUNNING"
	JobSucceeded JobStatus = "SUCCEEDED"
	JobFailed    JobStatus = "FAILED"
	JobTimedOut  JobStatus = "TIMED_OUT"
	JobCancelled JobStatus = "CANCELLED"
)

// IsActive reports whether the job still occupies its asset's slot for a mode.
// Migration 000012 allows at most one QUEUED or RUNNING job per tenant, asset
// and mode, so every place that reasons about "in flight" must agree on this
// set: the scheduler before queueing, the memory store when mirroring the
// index, and the SQL predicate itself.
func (s JobStatus) IsActive() bool {
	return s == JobQueued || s == JobRunning
}

type IncidentStatus string

const (
	IncidentOpen          IncidentStatus = "OPEN"
	IncidentAcknowledged  IncidentStatus = "ACKNOWLEDGED"
	IncidentInvestigating IncidentStatus = "INVESTIGATING"
	IncidentResolved      IncidentStatus = "RESOLVED"
	IncidentClosed        IncidentStatus = "CLOSED"
	IncidentFalsePositive IncidentStatus = "FALSE_POSITIVE"
)

type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

type Tenant struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"createdAt"`
}

type User struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Site struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"`
	Domain    string    `json:"domain"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Asset struct {
	ID                    string             `json:"id"`
	TenantID              string             `json:"tenantId"`
	SiteID                string             `json:"siteId,omitempty"`
	Name                  string             `json:"name"`
	URL                   string             `json:"url"`
	Type                  AssetType          `json:"type"`
	OwnerID               string             `json:"ownerId"`
	Enabled               bool               `json:"enabled"`
	VerificationStatus    VerificationStatus `json:"verificationStatus"`
	VerificationMethod    VerificationMethod `json:"verificationMethod"`
	VerificationToken     string             `json:"-"`
	VerificationTokenHash string             `json:"-"`
	VerificationIssuedAt  *time.Time         `json:"verificationIssuedAt,omitempty"`
	VerificationExpiresAt *time.Time         `json:"verificationExpiresAt,omitempty"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
}

// AuthConfig enables login-state detection: the checker authenticates against
// LoginURL before capturing the protected asset, so the snapshot reflects the
// authenticated page rather than the login form. Username and password are only
// ever stored encrypted (AES-GCM with associated data bound to tenant+asset);
// they are never returned by the API, logged, or written to spans.
type AuthConfig struct {
	LoginURL          string            `json:"loginUrl"`
	UsernameField     string            `json:"usernameField"`
	PasswordField     string            `json:"passwordField"`
	ExtraFields       map[string]string `json:"extraFields,omitempty"`
	SubmitSelector    string            `json:"submitSelector,omitempty"`
	EncryptedUsername string            `json:"encryptedUsername,omitempty"`
	EncryptedPassword string            `json:"encryptedPassword,omitempty"`
	CredentialsSet    bool              `json:"credentialsSet,omitempty"`
}

type MonitorPolicy struct {
	ID                     string      `json:"id"`
	TenantID               string      `json:"tenantId"`
	AssetID                string      `json:"assetId"`
	Version                int         `json:"version"`
	HTTPIntervalSeconds    int         `json:"httpIntervalSeconds"`
	BrowserIntervalSeconds int         `json:"browserIntervalSeconds"`
	TimeoutSeconds         int         `json:"timeoutSeconds"`
	MaxResponseBytes       int64       `json:"maxResponseBytes"`
	IgnoreSelectors        []string    `json:"ignoreSelectors"`
	TrustedDomains         []string    `json:"trustedDomains"`
	MaintenanceWindowStart *time.Time  `json:"maintenanceWindowStart,omitempty"`
	MaintenanceWindowEnd   *time.Time  `json:"maintenanceWindowEnd,omitempty"`
	Auth                   *AuthConfig `json:"auth,omitempty"`
	CreatedAt              time.Time   `json:"createdAt"`
	UpdatedAt              time.Time   `json:"updatedAt"`
}

type ResourceRef struct {
	URL        string `json:"url,omitempty"`
	InlineHash string `json:"inlineHash,omitempty"`
}

type FormRef struct {
	Action string `json:"action"`
	Method string `json:"method"`
}

type Snapshot struct {
	URL              string              `json:"url"`
	FinalURL         string              `json:"finalUrl"`
	StatusCode       int                 `json:"statusCode"`
	Title            string              `json:"title"`
	ContentHash      string              `json:"contentHash"`
	TextHash         string              `json:"textHash"`
	ScreenshotHash   string              `json:"screenshotHash,omitempty"`
	Scripts          []ResourceRef       `json:"scripts"`
	Links            []string            `json:"links"`
	IFrames          []string            `json:"iframes"`
	Forms            []FormRef           `json:"forms"`
	RedirectChain    []string            `json:"redirectChain"`
	ResolvedIPs      []string            `json:"resolvedIps"`
	TLSFingerprint   string              `json:"tlsFingerprint,omitempty"`
	ResponseHeaders  map[string][]string `json:"responseHeaders"`
	ResourceRequests []string            `json:"resourceRequests,omitempty"`
	BlockedRequests  []string            `json:"blockedRequests,omitempty"`
	Popups           []string            `json:"popups,omitempty"`
	Dialogs          []string            `json:"dialogs,omitempty"`
	Downloads        []string            `json:"downloads,omitempty"`
	DurationMillis   int64               `json:"durationMillis"`
	CapturedAt       time.Time           `json:"capturedAt"`
}

type BaselineVersion struct {
	ID          string         `json:"id"`
	TenantID    string         `json:"tenantId"`
	AssetID     string         `json:"assetId"`
	Version     int            `json:"version"`
	Status      BaselineStatus `json:"status"`
	Snapshot    Snapshot       `json:"snapshot"`
	EvidenceIDs []string       `json:"evidenceIds"`
	CreatedBy   string         `json:"createdBy"`
	ApprovedBy  string         `json:"approvedBy,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	ApprovedAt  *time.Time     `json:"approvedAt,omitempty"`
}

type DetectionJob struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenantId"`
	AssetID         string     `json:"assetId"`
	Mode            string     `json:"mode"`
	Status          JobStatus  `json:"status"`
	PolicyVersion   int        `json:"policyVersion"`
	BaselineVersion int        `json:"baselineVersion,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	RetryCount      int        `json:"retryCount"`
	MaxAttempts     int        `json:"maxAttempts"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

type Signal struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary"`
	Before      any      `json:"before,omitempty"`
	After       any      `json:"after,omitempty"`
	EvidenceIDs []string `json:"evidenceIds"`
	RuleVersion string   `json:"ruleVersion"`
	Fingerprint string   `json:"fingerprint"`
}

type Evidence struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	AssetID     string     `json:"assetId"`
	JobID       string     `json:"jobId"`
	Kind        string     `json:"kind"`
	MediaType   string     `json:"mediaType"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	ObjectKey   string     `json:"-"`
	CreatedAt   time.Time  `json:"createdAt"`
	RetainUntil *time.Time `json:"retainUntil,omitempty"`
}

type Incident struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"tenantId"`
	AssetID         string         `json:"assetId"`
	BaselineID      string         `json:"baselineId"`
	BaselineVersion int            `json:"baselineVersion"`
	Fingerprint     string         `json:"fingerprint"`
	Severity        Severity       `json:"severity"`
	Status          IncidentStatus `json:"status"`
	Title           string         `json:"title"`
	Signals         []Signal       `json:"signals"`
	EvidenceIDs     []string       `json:"evidenceIds"`
	OccurrenceCount int            `json:"occurrenceCount"`
	OwnerID         string         `json:"ownerId,omitempty"`
	Resolution      string         `json:"resolution,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	FirstSeenAt     time.Time      `json:"firstSeenAt"`
	LastSeenAt      time.Time      `json:"lastSeenAt"`
	AcknowledgedAt  *time.Time     `json:"acknowledgedAt,omitempty"`
	ResolvedAt      *time.Time     `json:"resolvedAt,omitempty"`
	// JobID is the detection job that produced this round. It is input only:
	// the store records it on the timeline entry and never writes it to the
	// incident row, so it stays out of the JSON contract.
	JobID string `json:"-"`
}

type IncidentEventType string

const (
	IncidentEventCreated       IncidentEventType = "CREATED"
	IncidentEventRecurred      IncidentEventType = "RECURRED"
	IncidentEventStatusChanged IncidentEventType = "STATUS_CHANGED"
	IncidentEventOwnerAssigned IncidentEventType = "OWNER_ASSIGNED"
	IncidentEventCommented     IncidentEventType = "COMMENTED"
)

// IncidentEvent is one append-only entry in an incident's timeline.
//
// It exists because the incident row cannot hold its own history: the upsert
// overwrites `signals` on every recurrence, so before this the second and later
// occurrences survived only as a counter. Signatures are per-occurrence here --
// Signals is what was seen during that one detection, not the set that happens
// to be current now.
type IncidentEvent struct {
	ID         string            `json:"id"`
	TenantID   string            `json:"tenantId"`
	IncidentID string            `json:"incidentId"`
	Type       IncidentEventType `json:"type"`
	// Empty when the detection worker wrote the entry on its own; an automated
	// recurrence has no human actor behind it.
	ActorID     string         `json:"actorId,omitempty"`
	FromStatus  IncidentStatus `json:"fromStatus,omitempty"`
	ToStatus    IncidentStatus `json:"toStatus,omitempty"`
	Severity    Severity       `json:"severity,omitempty"`
	Title       string         `json:"title,omitempty"`
	Signals     []Signal       `json:"signals,omitempty"`
	EvidenceIDs []string       `json:"evidenceIds,omitempty"`
	// JobID links a detection-driven entry back to the job that produced it,
	// which is what makes a timeline entry traceable to its run.
	JobID     string         `json:"jobId,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type AlertChannel struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	URL        string    `json:"url"`
	Recipients string    `json:"recipients,omitempty"`
	Secret     string    `json:"-"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type AlertDelivery struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenantId"`
	IncidentID    string     `json:"incidentId"`
	ChannelID     string     `json:"channelId"`
	DedupKey      string     `json:"dedupKey"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type AlertJob struct {
	TenantID   string `json:"tenantId"`
	IncidentID string `json:"incidentId"`
	AssetID    string `json:"assetId"`
	// Reason names what happened to the incident, and is part of the delivery's
	// dedup key: one incident now notifies more than once -- it is opened, then
	// acknowledged, then resolved -- and those are different messages about the
	// same row. Without the reason in the key the later ones would be read as
	// duplicates of the first and silently dropped.
	//
	// It is a plain string rather than a closed type because the set spans two
	// packages: detection raises "RECURRED" and the API raises the transition
	// reasons, and neither should have to import the other to add one.
	Reason string `json:"reason,omitempty"`
}

// Alert reasons carried on AlertJob.Reason. They are not a closed set; these are
// the ones the code currently raises.
const (
	// AlertReasonCreated is a newly opened incident.
	AlertReasonCreated = "CREATED"
	// AlertReasonRecurred is a fingerprint that matched an incident already on
	// the books -- the page was tampered with again after being looked at.
	AlertReasonRecurred = "RECURRED"
	// AlertReasonTransitioned is a status change an operator made by hand.
	AlertReasonTransitioned = "TRANSITIONED"
	// AlertReasonCommented is a note added to an incident's timeline.
	AlertReasonCommented = "COMMENTED"
)

type AuditEvent struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenantId"`
	ActorID      string         `json:"actorId"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	Result       string         `json:"result"`
	TraceID      string         `json:"traceId"`
	Details      map[string]any `json:"details,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
}
