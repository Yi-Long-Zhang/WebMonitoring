package store

import (
	"errors"
	"time"

	"webmonitoring/internal/domain"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource conflicts with existing state")
)

type Repository interface {
	CreateUser(domain.User) error
	FindUserByUsername(tenantID, username string) (domain.User, error)
	GetUser(tenantID, id string) (domain.User, error)

	CreateAsset(domain.Asset) error
	UpdateAsset(domain.Asset) error
	GetAsset(tenantID, id string) (domain.Asset, error)
	ListAssets(tenantID string) ([]domain.Asset, error)

	UpsertPolicy(domain.MonitorPolicy) error
	GetPolicy(tenantID, assetID string) (domain.MonitorPolicy, error)
	ListPolicies(tenantID string) ([]domain.MonitorPolicy, error)

	CreateBaseline(domain.BaselineVersion) error
	UpdateBaseline(domain.BaselineVersion) error
	GetBaseline(tenantID, id string) (domain.BaselineVersion, error)
	GetApprovedBaseline(tenantID, assetID string) (domain.BaselineVersion, error)
	ListBaselines(tenantID, assetID string) ([]domain.BaselineVersion, error)

	CreateJob(domain.DetectionJob) error
	ClaimJob(tenantID, id string, startedAt time.Time) (bool, error)
	UpdateJob(domain.DetectionJob) error
	GetJob(tenantID, id string) (domain.DetectionJob, error)
	ListJobs(tenantID string) ([]domain.DetectionJob, error)

	CreateEvidence(domain.Evidence) error
	GetEvidence(tenantID, id string) (domain.Evidence, error)
	ListEvidence(tenantID, assetID string) ([]domain.Evidence, error)
	ListExpiredEvidence(tenantID string, before time.Time) ([]domain.Evidence, error)
	IsEvidenceReferenced(tenantID, id string) (bool, error)
	DeleteEvidence(tenantID, id string) error

	UpsertIncident(domain.Incident) (domain.Incident, bool, error)
	UpdateIncident(domain.Incident) error
	GetIncident(tenantID, id string) (domain.Incident, error)
	ListIncidents(tenantID string) ([]domain.Incident, error)

	CreateAlertChannel(domain.AlertChannel) error
	ListAlertChannels(tenantID string) ([]domain.AlertChannel, error)
	ListAlertChannelsWithSecrets(tenantID string) ([]domain.AlertChannel, error)
	CreateAlertDelivery(domain.AlertDelivery) error
	UpdateAlertDelivery(domain.AlertDelivery) error
	ListAlertDeliveries(tenantID, incidentID string) ([]domain.AlertDelivery, error)

	AppendAudit(domain.AuditEvent) error
	ListAudits(tenantID string) ([]domain.AuditEvent, error)
	DeleteAuditsBefore(tenantID string, before time.Time) (int, error)
}
