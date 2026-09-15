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

type MonitorPolicy struct {
	ID                     string     `json:"id"`
	TenantID               string     `json:"tenantId"`
	AssetID                string     `json:"assetId"`
	Version                int        `json:"version"`
	HTTPIntervalSeconds    int        `json:"httpIntervalSeconds"`
	BrowserIntervalSeconds int        `json:"browserIntervalSeconds"`
	TimeoutSeconds         int        `json:"timeoutSeconds"`
	MaxResponseBytes       int64      `json:"maxResponseBytes"`
	IgnoreSelectors        []string   `json:"ignoreSelectors"`
	TrustedDomains         []string   `json:"trustedDomains"`
	MaintenanceWindowStart *time.Time `json:"maintenanceWindowStart,omitempty"`
	MaintenanceWindowEnd   *time.Time `json:"maintenanceWindowEnd,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
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
}

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
