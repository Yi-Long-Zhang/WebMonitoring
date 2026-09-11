package store

import (
	"sort"
	"strings"
	"sync"

	"webmonitoring/internal/domain"
)

type MemoryRepository struct {
	mu        sync.RWMutex
	users     map[string]domain.User
	assets    map[string]domain.Asset
	policies  map[string]domain.MonitorPolicy
	baselines map[string]domain.BaselineVersion
	jobs      map[string]domain.DetectionJob
	evidence  map[string]domain.Evidence
	incidents map[string]domain.Incident
	channels  map[string]domain.AlertChannel
	audits    []domain.AuditEvent
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		users:     make(map[string]domain.User),
		assets:    make(map[string]domain.Asset),
		policies:  make(map[string]domain.MonitorPolicy),
		baselines: make(map[string]domain.BaselineVersion),
		jobs:      make(map[string]domain.DetectionJob),
		evidence:  make(map[string]domain.Evidence),
		incidents: make(map[string]domain.Incident),
		channels:  make(map[string]domain.AlertChannel),
	}
}

func tenantKey(tenantID, id string) string {
	return tenantID + ":" + id
}

func (m *MemoryRepository) CreateUser(user domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.users {
		if existing.TenantID == user.TenantID && strings.EqualFold(existing.Username, user.Username) {
			return ErrConflict
		}
	}
	m.users[tenantKey(user.TenantID, user.ID)] = user
	return nil
}

func (m *MemoryRepository) FindUserByUsername(tenantID, username string) (domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.TenantID == tenantID && strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}

func (m *MemoryRepository) GetUser(tenantID, id string) (domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[tenantKey(tenantID, id)]
	if !ok {
		return domain.User{}, ErrNotFound
	}
	return user, nil
}

func (m *MemoryRepository) CreateAsset(asset domain.Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.assets {
		if existing.TenantID == asset.TenantID && strings.EqualFold(existing.URL, asset.URL) {
			return ErrConflict
		}
	}
	m.assets[tenantKey(asset.TenantID, asset.ID)] = asset
	return nil
}

func (m *MemoryRepository) UpdateAsset(asset domain.Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(asset.TenantID, asset.ID)
	if _, ok := m.assets[key]; !ok {
		return ErrNotFound
	}
	m.assets[key] = asset
	return nil
}

func (m *MemoryRepository) GetAsset(tenantID, id string) (domain.Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	asset, ok := m.assets[tenantKey(tenantID, id)]
	if !ok {
		return domain.Asset{}, ErrNotFound
	}
	return asset, nil
}

func (m *MemoryRepository) ListAssets(tenantID string) []domain.Asset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Asset, 0)
	for _, asset := range m.assets {
		if asset.TenantID == tenantID {
			result = append(result, asset)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *MemoryRepository) UpsertPolicy(policy domain.MonitorPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies[tenantKey(policy.TenantID, policy.AssetID)] = policy
	return nil
}

func (m *MemoryRepository) GetPolicy(tenantID, assetID string) (domain.MonitorPolicy, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	policy, ok := m.policies[tenantKey(tenantID, assetID)]
	if !ok {
		return domain.MonitorPolicy{}, ErrNotFound
	}
	return clonePolicy(policy), nil
}

func (m *MemoryRepository) ListPolicies(tenantID string) []domain.MonitorPolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.MonitorPolicy, 0)
	for _, policy := range m.policies {
		if policy.TenantID == tenantID {
			result = append(result, clonePolicy(policy))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result
}

func (m *MemoryRepository) CreateBaseline(baseline domain.BaselineVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(baseline.TenantID, baseline.ID)
	if _, ok := m.baselines[key]; ok {
		return ErrConflict
	}
	m.baselines[key] = cloneBaseline(baseline)
	return nil
}

func (m *MemoryRepository) UpdateBaseline(baseline domain.BaselineVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(baseline.TenantID, baseline.ID)
	if _, ok := m.baselines[key]; !ok {
		return ErrNotFound
	}
	if baseline.Status == domain.BaselineApproved {
		for otherKey, current := range m.baselines {
			if current.TenantID == baseline.TenantID && current.AssetID == baseline.AssetID &&
				current.ID != baseline.ID && current.Status == domain.BaselineApproved {
				current.Status = domain.BaselineRetired
				m.baselines[otherKey] = current
			}
		}
	}
	m.baselines[key] = cloneBaseline(baseline)
	return nil
}

func (m *MemoryRepository) GetBaseline(tenantID, id string) (domain.BaselineVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	baseline, ok := m.baselines[tenantKey(tenantID, id)]
	if !ok {
		return domain.BaselineVersion{}, ErrNotFound
	}
	return cloneBaseline(baseline), nil
}

func (m *MemoryRepository) GetApprovedBaseline(tenantID, assetID string) (domain.BaselineVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var selected domain.BaselineVersion
	found := false
	for _, baseline := range m.baselines {
		if baseline.TenantID == tenantID && baseline.AssetID == assetID && baseline.Status == domain.BaselineApproved {
			if !found || baseline.Version > selected.Version {
				selected = baseline
				found = true
			}
		}
	}
	if !found {
		return domain.BaselineVersion{}, ErrNotFound
	}
	return cloneBaseline(selected), nil
}

func (m *MemoryRepository) ListBaselines(tenantID, assetID string) []domain.BaselineVersion {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.BaselineVersion, 0)
	for _, baseline := range m.baselines {
		if baseline.TenantID == tenantID && (assetID == "" || baseline.AssetID == assetID) {
			result = append(result, cloneBaseline(baseline))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AssetID == result[j].AssetID {
			return result[i].Version > result[j].Version
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

func (m *MemoryRepository) CreateJob(job domain.DetectionJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(job.TenantID, job.ID)
	if _, ok := m.jobs[key]; ok {
		return ErrConflict
	}
	m.jobs[key] = job
	return nil
}

func (m *MemoryRepository) UpdateJob(job domain.DetectionJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(job.TenantID, job.ID)
	if _, ok := m.jobs[key]; !ok {
		return ErrNotFound
	}
	m.jobs[key] = job
	return nil
}

func (m *MemoryRepository) GetJob(tenantID, id string) (domain.DetectionJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[tenantKey(tenantID, id)]
	if !ok {
		return domain.DetectionJob{}, ErrNotFound
	}
	return job, nil
}

func (m *MemoryRepository) ListJobs(tenantID string) []domain.DetectionJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.DetectionJob, 0)
	for _, job := range m.jobs {
		if job.TenantID == tenantID {
			result = append(result, job)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *MemoryRepository) CreateEvidence(evidence domain.Evidence) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(evidence.TenantID, evidence.ID)
	if _, ok := m.evidence[key]; ok {
		return ErrConflict
	}
	m.evidence[key] = evidence
	return nil
}

func (m *MemoryRepository) GetEvidence(tenantID, id string) (domain.Evidence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	evidence, ok := m.evidence[tenantKey(tenantID, id)]
	if !ok {
		return domain.Evidence{}, ErrNotFound
	}
	return evidence, nil
}

func (m *MemoryRepository) ListEvidence(tenantID, assetID string) []domain.Evidence {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Evidence, 0)
	for _, evidence := range m.evidence {
		if evidence.TenantID == tenantID && (assetID == "" || evidence.AssetID == assetID) {
			result = append(result, evidence)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *MemoryRepository) UpsertIncident(incident domain.Incident) (domain.Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, current := range m.incidents {
		if current.TenantID == incident.TenantID && current.AssetID == incident.AssetID &&
			current.BaselineVersion == incident.BaselineVersion && current.Fingerprint == incident.Fingerprint &&
			current.Status != domain.IncidentClosed && current.Status != domain.IncidentFalsePositive {
			current.LastSeenAt = incident.LastSeenAt
			current.OccurrenceCount++
			current.Signals = append([]domain.Signal(nil), incident.Signals...)
			current.EvidenceIDs = appendUnique(current.EvidenceIDs, incident.EvidenceIDs...)
			if severityRank(incident.Severity) > severityRank(current.Severity) {
				current.Severity = incident.Severity
			}
			m.incidents[key] = current
			return cloneIncident(current), false, nil
		}
	}
	m.incidents[tenantKey(incident.TenantID, incident.ID)] = cloneIncident(incident)
	return cloneIncident(incident), true, nil
}

func (m *MemoryRepository) UpdateIncident(incident domain.Incident) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(incident.TenantID, incident.ID)
	if _, ok := m.incidents[key]; !ok {
		return ErrNotFound
	}
	m.incidents[key] = cloneIncident(incident)
	return nil
}

func (m *MemoryRepository) GetIncident(tenantID, id string) (domain.Incident, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	incident, ok := m.incidents[tenantKey(tenantID, id)]
	if !ok {
		return domain.Incident{}, ErrNotFound
	}
	return cloneIncident(incident), nil
}

func (m *MemoryRepository) ListIncidents(tenantID string) []domain.Incident {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Incident, 0)
	for _, incident := range m.incidents {
		if incident.TenantID == tenantID {
			result = append(result, cloneIncident(incident))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastSeenAt.After(result[j].LastSeenAt) })
	return result
}

func (m *MemoryRepository) CreateAlertChannel(channel domain.AlertChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(channel.TenantID, channel.ID)
	if _, ok := m.channels[key]; ok {
		return ErrConflict
	}
	m.channels[key] = channel
	return nil
}

func (m *MemoryRepository) ListAlertChannels(tenantID string) []domain.AlertChannel {
	result := m.ListAlertChannelsWithSecrets(tenantID)
	for index := range result {
		result[index].Secret = ""
	}
	return result
}

func (m *MemoryRepository) ListAlertChannelsWithSecrets(tenantID string) []domain.AlertChannel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.AlertChannel, 0)
	for _, channel := range m.channels {
		if channel.TenantID == tenantID {
			result = append(result, channel)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (m *MemoryRepository) AppendAudit(event domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, event)
	return nil
}

func (m *MemoryRepository) ListAudits(tenantID string) []domain.AuditEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.AuditEvent, 0)
	for _, event := range m.audits {
		if event.TenantID == tenantID {
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func clonePolicy(policy domain.MonitorPolicy) domain.MonitorPolicy {
	policy.IgnoreSelectors = append([]string(nil), policy.IgnoreSelectors...)
	policy.TrustedDomains = append([]string(nil), policy.TrustedDomains...)
	return policy
}

func cloneBaseline(baseline domain.BaselineVersion) domain.BaselineVersion {
	baseline.EvidenceIDs = append([]string(nil), baseline.EvidenceIDs...)
	baseline.Snapshot.Scripts = append([]domain.ResourceRef(nil), baseline.Snapshot.Scripts...)
	baseline.Snapshot.Links = append([]string(nil), baseline.Snapshot.Links...)
	baseline.Snapshot.IFrames = append([]string(nil), baseline.Snapshot.IFrames...)
	baseline.Snapshot.Forms = append([]domain.FormRef(nil), baseline.Snapshot.Forms...)
	baseline.Snapshot.RedirectChain = append([]string(nil), baseline.Snapshot.RedirectChain...)
	baseline.Snapshot.ResolvedIPs = append([]string(nil), baseline.Snapshot.ResolvedIPs...)
	return baseline
}

func cloneIncident(incident domain.Incident) domain.Incident {
	incident.Signals = append([]domain.Signal(nil), incident.Signals...)
	incident.EvidenceIDs = append([]string(nil), incident.EvidenceIDs...)
	return incident
}

func appendUnique(existing []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, item := range existing {
		seen[item] = struct{}{}
	}
	for _, item := range additions {
		if _, ok := seen[item]; !ok {
			existing = append(existing, item)
			seen[item] = struct{}{}
		}
	}
	return existing
}

func severityRank(value domain.Severity) int {
	switch value {
	case domain.SeverityCritical:
		return 5
	case domain.SeverityHigh:
		return 4
	case domain.SeverityMedium:
		return 3
	case domain.SeverityLow:
		return 2
	default:
		return 1
	}
}
