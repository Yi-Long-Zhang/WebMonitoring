package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

type MemoryRepository struct {
	// jobLease is the RUNNING claim lease. See Repository.JobLease.
	jobLease  time.Duration
	mu        sync.RWMutex
	tenants   map[string]domain.Tenant
	sites     map[string]domain.Site
	users     map[string]domain.User
	assets    map[string]domain.Asset
	policies  map[string]domain.MonitorPolicy
	baselines map[string]domain.BaselineVersion
	jobs      map[string]domain.DetectionJob
	evidence  map[string]domain.Evidence
	incidents map[string]domain.Incident
	// incidentEvents is append-only, like audits: the timeline is a history, so
	// entries are never rewritten or removed.
	incidentEvents []domain.IncidentEvent
	channels       map[string]domain.AlertChannel
	deliveries     map[string]domain.AlertDelivery
	audits         []domain.AuditEvent
}

func NewMemoryRepository() *MemoryRepository {
	repo := &MemoryRepository{
		jobLease:   JobLease,
		tenants:    make(map[string]domain.Tenant),
		sites:      make(map[string]domain.Site),
		users:      make(map[string]domain.User),
		assets:     make(map[string]domain.Asset),
		policies:   make(map[string]domain.MonitorPolicy),
		baselines:  make(map[string]domain.BaselineVersion),
		jobs:       make(map[string]domain.DetectionJob),
		evidence:   make(map[string]domain.Evidence),
		incidents:  make(map[string]domain.Incident),
		channels:   make(map[string]domain.AlertChannel),
		deliveries: make(map[string]domain.AlertDelivery),
	}
	repo.tenants[domain.DefaultTenantID] = domain.Tenant{
		ID: domain.DefaultTenantID, Slug: "default", Name: "Default Tenant",
	}
	return repo
}

func (m *MemoryRepository) Ready(context.Context) error {
	return nil
}

func (m *MemoryRepository) ListTenants(opts ListOptions) ([]domain.Tenant, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Tenant, 0, len(m.tenants))
	for _, tenant := range m.tenants {
		result = append(result, tenant)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(t domain.Tenant) (time.Time, string) { return t.CreatedAt, t.ID })
	return items, next, nil
}

func (m *MemoryRepository) GetTenant(tenantID string) (domain.Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tenant, ok := m.tenants[tenantID]
	if !ok {
		return domain.Tenant{}, ErrNotFound
	}
	return tenant, nil
}

func (m *MemoryRepository) GetTenantBySlug(slug string) (domain.Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, tenant := range m.tenants {
		if strings.EqualFold(tenant.Slug, slug) {
			return tenant, nil
		}
	}
	return domain.Tenant{}, ErrNotFound
}

func (m *MemoryRepository) CreateTenant(tenant domain.Tenant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.tenants {
		if strings.EqualFold(existing.Slug, tenant.Slug) {
			return ErrConflict
		}
	}
	m.tenants[tenant.ID] = tenant
	return nil
}

func (m *MemoryRepository) UpdateTenant(tenant domain.Tenant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[tenant.ID]; !ok {
		return ErrNotFound
	}
	for _, existing := range m.tenants {
		if existing.ID != tenant.ID && strings.EqualFold(existing.Slug, tenant.Slug) {
			return ErrConflict
		}
	}
	m.tenants[tenant.ID] = tenant
	return nil
}

func tenantKey(tenantID, id string) string {
	return tenantID + ":" + id
}

// CountTenantResources returns per-kind row counts for one tenant. The JSON
// keys match the Postgres implementation.
func (m *MemoryRepository) CountTenantResources(tenantID string) (map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := map[string]int{"assets": 0, "sites": 0, "users": 0, "evidence": 0,
		"incidents": 0, "baselines": 0, "jobs": 0, "alertChannels": 0, "auditEvents": 0,
		"incidentEvents": 0}
	for _, site := range m.sites {
		if site.TenantID == tenantID {
			result["sites"]++
		}
	}
	for _, user := range m.users {
		if user.TenantID == tenantID {
			result["users"]++
		}
	}
	for _, asset := range m.assets {
		if asset.TenantID == tenantID {
			result["assets"]++
		}
	}
	for _, baseline := range m.baselines {
		if baseline.TenantID == tenantID {
			result["baselines"]++
		}
	}
	for _, job := range m.jobs {
		if job.TenantID == tenantID {
			result["jobs"]++
		}
	}
	for _, item := range m.evidence {
		if item.TenantID == tenantID {
			result["evidence"]++
		}
	}
	for _, incident := range m.incidents {
		if incident.TenantID == tenantID {
			result["incidents"]++
		}
	}
	for _, channel := range m.channels {
		if channel.TenantID == tenantID {
			result["alertChannels"]++
		}
	}
	for _, event := range m.audits {
		if event.TenantID == tenantID {
			result["auditEvents"]++
		}
	}
	for _, event := range m.incidentEvents {
		if event.TenantID == tenantID {
			result["incidentEvents"]++
		}
	}
	return result, nil
}

// DeleteTenant removes a tenant and every tenant-scoped row in one critical
// section. Callers must have verified the tenant is empty (or explicitly
// requested a cascade) before calling.
func (m *MemoryRepository) DeleteTenant(tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[tenantID]; !ok {
		return ErrNotFound
	}
	delete(m.tenants, tenantID)
	for key, value := range m.sites {
		if value.TenantID == tenantID {
			delete(m.sites, key)
		}
	}
	for key, value := range m.users {
		if value.TenantID == tenantID {
			delete(m.users, key)
		}
	}
	for key, value := range m.assets {
		if value.TenantID == tenantID {
			delete(m.assets, key)
		}
	}
	for key, value := range m.policies {
		if value.TenantID == tenantID {
			delete(m.policies, key)
		}
	}
	for key, value := range m.baselines {
		if value.TenantID == tenantID {
			delete(m.baselines, key)
		}
	}
	for key, value := range m.jobs {
		if value.TenantID == tenantID {
			delete(m.jobs, key)
		}
	}
	for key, value := range m.evidence {
		if value.TenantID == tenantID {
			delete(m.evidence, key)
		}
	}
	for key, value := range m.incidents {
		if value.TenantID == tenantID {
			delete(m.incidents, key)
		}
	}
	for key, value := range m.channels {
		if value.TenantID == tenantID {
			delete(m.channels, key)
		}
	}
	for key, value := range m.deliveries {
		if value.TenantID == tenantID {
			delete(m.deliveries, key)
		}
	}
	filtered := m.audits[:0]
	for _, event := range m.audits {
		if event.TenantID != tenantID {
			filtered = append(filtered, event)
		}
	}
	m.audits = filtered
	keptEvents := m.incidentEvents[:0]
	for _, event := range m.incidentEvents {
		if event.TenantID != tenantID {
			keptEvents = append(keptEvents, event)
		}
	}
	m.incidentEvents = keptEvents
	return nil
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

func (m *MemoryRepository) ListUsers(tenantID string) ([]domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.User, 0)
	for _, user := range m.users {
		if user.TenantID == tenantID {
			result = append(result, user)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (m *MemoryRepository) UpdateUser(user domain.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(user.TenantID, user.ID)
	if _, ok := m.users[key]; !ok {
		return ErrNotFound
	}
	for _, existing := range m.users {
		if existing.TenantID == user.TenantID && existing.ID != user.ID && strings.EqualFold(existing.Username, user.Username) {
			return ErrConflict
		}
	}
	m.users[key] = user
	return nil
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

func (m *MemoryRepository) ListAssets(tenantID string, opts ListOptions) ([]domain.Asset, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Asset, 0)
	for _, asset := range m.assets {
		if asset.TenantID == tenantID {
			result = append(result, asset)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(a domain.Asset) (time.Time, string) { return a.CreatedAt, a.ID })
	return items, next, nil
}

func (m *MemoryRepository) CreateSite(site domain.Site) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.sites {
		if existing.TenantID == site.TenantID && strings.EqualFold(existing.Name, site.Name) {
			return ErrConflict
		}
	}
	m.sites[tenantKey(site.TenantID, site.ID)] = site
	return nil
}

func (m *MemoryRepository) UpdateSite(site domain.Site) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(site.TenantID, site.ID)
	if _, ok := m.sites[key]; !ok {
		return ErrNotFound
	}
	for otherKey, existing := range m.sites {
		if otherKey != key && existing.TenantID == site.TenantID && strings.EqualFold(existing.Name, site.Name) {
			return ErrConflict
		}
	}
	m.sites[key] = site
	return nil
}

func (m *MemoryRepository) GetSite(tenantID, id string) (domain.Site, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	site, ok := m.sites[tenantKey(tenantID, id)]
	if !ok {
		return domain.Site{}, ErrNotFound
	}
	return site, nil
}

func (m *MemoryRepository) ListSites(tenantID string, opts ListOptions) ([]domain.Site, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Site, 0)
	for _, site := range m.sites {
		if site.TenantID == tenantID {
			result = append(result, site)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(s domain.Site) (time.Time, string) { return s.CreatedAt, s.ID })
	return items, next, nil
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

func (m *MemoryRepository) ListPolicies(tenantID string, opts ListOptions) ([]domain.MonitorPolicy, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.MonitorPolicy, 0)
	for _, policy := range m.policies {
		if policy.TenantID == tenantID {
			result = append(result, clonePolicy(policy))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	items, next := applyPage(result, opts, func(p domain.MonitorPolicy) (time.Time, string) { return p.UpdatedAt, p.ID })
	return items, next, nil
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

func (m *MemoryRepository) ListBaselines(tenantID, assetID string, opts ListOptions) ([]domain.BaselineVersion, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.BaselineVersion, 0)
	for _, baseline := range m.baselines {
		if baseline.TenantID == tenantID && (assetID == "" || baseline.AssetID == assetID) {
			result = append(result, cloneBaseline(baseline))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(b domain.BaselineVersion) (time.Time, string) { return b.CreatedAt, b.ID })
	return items, next, nil
}

func (m *MemoryRepository) CreateJob(job domain.DetectionJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(job.TenantID, job.ID)
	if _, ok := m.jobs[key]; ok {
		return ErrConflict
	}
	// Mirror detection_jobs_active_uidx (migration 000012): at most one QUEUED
	// or RUNNING job per tenant, asset and mode.
	if job.Status.IsActive() {
		for _, existing := range m.jobs {
			if existing.TenantID == job.TenantID && existing.AssetID == job.AssetID &&
				existing.Mode == job.Mode && existing.Status.IsActive() {
				return ErrJobAlreadyActive
			}
		}
	}
	m.jobs[key] = job
	return nil
}

func (m *MemoryRepository) ClaimJob(tenantID, id string, startedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(tenantID, id)
	job, ok := m.jobs[key]
	if !ok {
		return false, ErrNotFound
	}
	staleRunning := job.Status == domain.JobRunning &&
		job.StartedAt != nil && job.StartedAt.Before(startedAt.Add(-m.jobLease))
	if job.Status != domain.JobQueued && !staleRunning {
		return false, nil
	}
	job.Status = domain.JobRunning
	job.StartedAt = &startedAt
	m.jobs[key] = job
	return true, nil
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

func (m *MemoryRepository) ListJobs(tenantID string, opts ListOptions) ([]domain.DetectionJob, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.DetectionJob, 0)
	for _, job := range m.jobs {
		if job.TenantID == tenantID {
			result = append(result, job)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(j domain.DetectionJob) (time.Time, string) { return j.CreatedAt, j.ID })
	return items, next, nil
}

func (m *MemoryRepository) CancelJob(tenantID, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(tenantID, id)
	job, ok := m.jobs[key]
	if !ok {
		return false, ErrNotFound
	}
	if job.Status != domain.JobQueued && job.Status != domain.JobRunning {
		return false, nil
	}
	now := time.Now().UTC()
	job.Status = domain.JobCancelled
	job.FinishedAt = &now
	m.jobs[key] = job
	return true, nil
}

func (m *MemoryRepository) ReapTimedOut(tenantID string, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reaped := 0
	for key, job := range m.jobs {
		if job.TenantID != tenantID || job.Status != domain.JobRunning {
			continue
		}
		if job.StartedAt == nil || !job.StartedAt.Before(before) {
			continue
		}
		now := time.Now().UTC()
		job.Status = domain.JobTimedOut
		job.ErrorCode = "CHECK_TIMEOUT"
		job.FinishedAt = &now
		m.jobs[key] = job
		reaped++
	}
	return reaped, nil
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

func (m *MemoryRepository) ListEvidence(tenantID, assetID string, opts ListOptions) ([]domain.Evidence, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Evidence, 0)
	for _, evidence := range m.evidence {
		if evidence.TenantID == tenantID && (assetID == "" || evidence.AssetID == assetID) {
			result = append(result, evidence)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(e domain.Evidence) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
}

func (m *MemoryRepository) ListExpiredEvidence(tenantID string, before time.Time) ([]domain.Evidence, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Evidence, 0)
	for _, item := range m.evidence {
		if item.TenantID == tenantID && item.RetainUntil != nil && !item.RetainUntil.After(before) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (m *MemoryRepository) IsEvidenceReferenced(tenantID, id string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, incident := range m.incidents {
		if incident.TenantID == tenantID && contains(incident.EvidenceIDs, id) {
			return true, nil
		}
	}
	return false, nil
}

func (m *MemoryRepository) DeleteEvidence(tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(tenantID, id)
	item, ok := m.evidence[key]
	if !ok {
		return ErrNotFound
	}
	for _, incident := range m.incidents {
		if incident.TenantID == tenantID && contains(incident.EvidenceIDs, item.ID) {
			return ErrConflict
		}
	}
	delete(m.evidence, key)
	return nil
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
			// Sorted, deduplicated merge, matching the jsonb aggregation the
			// PostgreSQL implementation performs. The two stores must agree on
			// content and order or the same input produces different incidents.
			current.EvidenceIDs = sortedUnique(current.EvidenceIDs, incident.EvidenceIDs...)
			// Monotonic, matching GREATEST in PostgreSQL: an incident does not
			// quietly de-escalate because one recurrence looked milder.
			if severityRank(incident.Severity) > severityRank(current.Severity) {
				current.Severity = incident.Severity
			}
			m.incidents[key] = current
			// Same critical section as the counter above, mirroring the single
			// statement the PostgreSQL implementation uses. Two separate writes
			// could interleave and lose the occurrence.
			m.incidentEvents = append(m.incidentEvents,
				occurrenceEvent(current, incident, domain.IncidentEventRecurred))
			return cloneIncident(current), false, nil
		}
	}
	m.incidents[tenantKey(incident.TenantID, incident.ID)] = cloneIncident(incident)
	m.incidentEvents = append(m.incidentEvents,
		occurrenceEvent(incident, incident, domain.IncidentEventCreated))
	return cloneIncident(incident), true, nil
}

// occurrenceEvent builds the timeline entry for one detection round.
//
// Signals and EvidenceIDs come from the round, not from the merged incident:
// `signals` on the incident row is overwritten every time, so what this round
// saw exists nowhere else. Severity comes from the merged row because that is
// the incident's risk level after this round.
func occurrenceEvent(merged, round domain.Incident, eventType domain.IncidentEventType) domain.IncidentEvent {
	return cloneIncidentEvent(domain.IncidentEvent{
		ID: identity.New(), TenantID: merged.TenantID, IncidentID: merged.ID,
		Type: eventType, Severity: merged.Severity, Title: round.Title,
		Signals: round.Signals, EvidenceIDs: round.EvidenceIDs,
		JobID: round.JobID, CreatedAt: round.CreatedAt,
	})
}

// AppendIncidentEvent refuses an incident that does not exist in this tenant.
// The PostgreSQL implementation gets the same answer from INSERT ... SELECT and
// a zero row count; the foreign key would report 23503, which the API surfaces
// as a 500 and which memory could not reproduce.
func (m *MemoryRepository) AppendIncidentEvent(event domain.IncidentEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.incidents[tenantKey(event.TenantID, event.IncidentID)]; !ok {
		return ErrNotFound
	}
	m.incidentEvents = append(m.incidentEvents, cloneIncidentEvent(event))
	return nil
}

func (m *MemoryRepository) ListIncidentEvents(tenantID, incidentID string, opts ListOptions) ([]domain.IncidentEvent, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.IncidentEvent, 0)
	for _, event := range m.incidentEvents {
		if event.TenantID == tenantID && event.IncidentID == incidentID {
			result = append(result, cloneIncidentEvent(event))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(e domain.IncidentEvent) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
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

func (m *MemoryRepository) ListIncidents(tenantID string, opts ListOptions) ([]domain.Incident, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Incident, 0)
	for _, incident := range m.incidents {
		if incident.TenantID == tenantID {
			result = append(result, cloneIncident(incident))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeenAt.Equal(result[j].LastSeenAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].LastSeenAt.After(result[j].LastSeenAt)
	})
	items, next := applyPage(result, opts, func(i domain.Incident) (time.Time, string) { return i.LastSeenAt, i.ID })
	return items, next, nil
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

func (m *MemoryRepository) GetAlertChannel(tenantID, id string) (domain.AlertChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, ok := m.channels[tenantKey(tenantID, id)]
	if !ok {
		return domain.AlertChannel{}, ErrNotFound
	}
	return channel, nil
}

func (m *MemoryRepository) UpdateAlertChannel(channel domain.AlertChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(channel.TenantID, channel.ID)
	if _, ok := m.channels[key]; !ok {
		return ErrNotFound
	}
	m.channels[key] = channel
	return nil
}

// RewrapAlertChannel replaces one channel's credentials in place. The memory
// repository stores plaintext, so this has nothing to re-seal; it exists so the
// key-rotation pass can run against the in-memory backend and so both
// implementations of Repository agree on the shape of the write.
func (m *MemoryRepository) RewrapAlertChannel(channel domain.AlertChannel) error {
	return m.UpdateAlertChannel(channel)
}

// RewrapPolicy replaces one policy in place, for the same reason.
func (m *MemoryRepository) RewrapPolicy(policy domain.MonitorPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(policy.TenantID, policy.AssetID)
	if _, ok := m.policies[key]; !ok {
		return ErrNotFound
	}
	m.policies[key] = clonePolicy(policy)
	return nil
}

func (m *MemoryRepository) DeleteAlertChannel(tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(tenantID, id)
	if _, ok := m.channels[key]; !ok {
		return ErrNotFound
	}
	delete(m.channels, key)
	return nil
}

func (m *MemoryRepository) ListAlertChannels(tenantID string) ([]domain.AlertChannel, error) {
	result, err := m.ListAlertChannelsWithSecrets(tenantID)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Secret = ""
	}
	return result, nil
}

func (m *MemoryRepository) ListAlertChannelsWithSecrets(tenantID string) ([]domain.AlertChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.AlertChannel, 0)
	for _, channel := range m.channels {
		if channel.TenantID == tenantID {
			result = append(result, channel)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (m *MemoryRepository) CreateAlertDelivery(delivery domain.AlertDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.deliveries {
		if existing.TenantID == delivery.TenantID && existing.DedupKey == delivery.DedupKey {
			return ErrConflict
		}
	}
	m.deliveries[tenantKey(delivery.TenantID, delivery.ID)] = delivery
	return nil
}

func (m *MemoryRepository) UpdateAlertDelivery(delivery domain.AlertDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantKey(delivery.TenantID, delivery.ID)
	if _, ok := m.deliveries[key]; !ok {
		return ErrNotFound
	}
	m.deliveries[key] = delivery
	return nil
}

func (m *MemoryRepository) ListAlertDeliveries(tenantID, incidentID string, opts ListOptions) ([]domain.AlertDelivery, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.AlertDelivery, 0)
	for _, delivery := range m.deliveries {
		if delivery.TenantID == tenantID && (incidentID == "" || delivery.IncidentID == incidentID) {
			result = append(result, delivery)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(d domain.AlertDelivery) (time.Time, string) { return d.CreatedAt, d.ID })
	return items, next, nil
}

func (m *MemoryRepository) AppendAudit(event domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, event)
	return nil
}

// JobLease reports the lease this repository enforces on a RUNNING claim.
func (m *MemoryRepository) JobLease() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobLease
}

// SetJobLease replaces the default lease. A non-positive value is ignored so a
// misread environment variable cannot make every claim immediately reclaimable.
func (m *MemoryRepository) SetJobLease(lease time.Duration) {
	if lease <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobLease = lease
}

func (m *MemoryRepository) ListAudits(tenantID string, filter AuditFilter, opts ListOptions) ([]domain.AuditEvent, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.AuditEvent, 0)
	for _, event := range m.audits {
		// Filter first, then sort, then page -- the same order of operations the
		// SQL takes. Applying the cursor before the filter would count rows the
		// caller cannot see and skip past visible ones.
		if event.TenantID == tenantID && filter.Matches(event) {
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	items, next := applyPage(result, opts, func(e domain.AuditEvent) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
}

func (m *MemoryRepository) DeleteAuditsBefore(tenantID string, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := make([]domain.AuditEvent, 0, len(m.audits))
	deleted := 0
	for _, event := range m.audits {
		if event.TenantID == tenantID && event.CreatedAt.Before(before) {
			deleted++
			continue
		}
		kept = append(kept, event)
	}
	m.audits = kept
	return deleted, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func clonePolicy(policy domain.MonitorPolicy) domain.MonitorPolicy {
	policy.IgnoreSelectors = append([]string(nil), policy.IgnoreSelectors...)
	policy.TrustedDomains = append([]string(nil), policy.TrustedDomains...)
	if policy.Auth != nil {
		auth := *policy.Auth
		auth.ExtraFields = cloneStringMap(policy.Auth.ExtraFields)
		policy.Auth = &auth
	}
	return policy
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneBaseline(baseline domain.BaselineVersion) domain.BaselineVersion {
	baseline.EvidenceIDs = append([]string(nil), baseline.EvidenceIDs...)
	baseline.Snapshot.Scripts = append([]domain.ResourceRef(nil), baseline.Snapshot.Scripts...)
	baseline.Snapshot.Links = append([]string(nil), baseline.Snapshot.Links...)
	baseline.Snapshot.IFrames = append([]string(nil), baseline.Snapshot.IFrames...)
	baseline.Snapshot.Forms = append([]domain.FormRef(nil), baseline.Snapshot.Forms...)
	baseline.Snapshot.RedirectChain = append([]string(nil), baseline.Snapshot.RedirectChain...)
	baseline.Snapshot.ResolvedIPs = append([]string(nil), baseline.Snapshot.ResolvedIPs...)
	baseline.Snapshot.ResourceRequests = append([]string(nil), baseline.Snapshot.ResourceRequests...)
	baseline.Snapshot.BlockedRequests = append([]string(nil), baseline.Snapshot.BlockedRequests...)
	baseline.Snapshot.Popups = append([]string(nil), baseline.Snapshot.Popups...)
	baseline.Snapshot.Dialogs = append([]string(nil), baseline.Snapshot.Dialogs...)
	baseline.Snapshot.Downloads = append([]string(nil), baseline.Snapshot.Downloads...)
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

// sortedUnique merges two id lists without duplicates, in sorted order.
//
// Sorted rather than insertion-ordered so it matches the jsonb aggregation the
// PostgreSQL upsert performs; the two stores must produce identical incident
// rows for identical input, and only one of them can control the order.
//
// Deduplication covers the union, not just the additions. appendUnique alone
// would miss a duplicate that was already inside `existing` (an incident row
// written before any merge), which is exactly the case the aggregation in
// PostgreSQL does collapse.
func sortedUnique(existing []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	merged := make([]string, 0, len(existing)+len(additions))
	for _, item := range append(append([]string(nil), existing...), additions...) {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		merged = append(merged, item)
	}
	sort.Strings(merged)
	return merged
}

func cloneIncidentEvent(event domain.IncidentEvent) domain.IncidentEvent {
	event.Signals = append([]domain.Signal(nil), event.Signals...)
	event.EvidenceIDs = append([]string(nil), event.EvidenceIDs...)
	if event.Details != nil {
		details := make(map[string]any, len(event.Details))
		for key, value := range event.Details {
			details[key] = value
		}
		event.Details = details
	}
	return event
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
