package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/security"
)

type PostgresRepository struct {
	// jobLease is the RUNNING claim lease. See Repository.JobLease.
	jobLease time.Duration
	pool     *pgxpool.Pool
	secrets  security.SecretBox
}

func NewPostgresRepository(ctx context.Context, dsn, alertSecretKey string, poolConfig PoolConfig) (*PostgresRepository, error) {
	secrets, err := security.NewKeyring(alertSecretKey)
	if err != nil {
		return nil, err
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	// A DSN can carry its own pool_size and connect_timeout. The explicit
	// configuration wins, because it is the one the production gate validated.
	parsed.MaxConns = poolConfig.MaxConns
	parsed.MinConns = poolConfig.MinConns
	parsed.MaxConnLifetime = poolConfig.MaxConnLifetime
	parsed.MaxConnIdleTime = poolConfig.MaxConnIdleTime
	parsed.HealthCheckPeriod = poolConfig.HealthCheckPeriod
	parsed.ConnConfig.ConnectTimeout = poolConfig.ConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &PostgresRepository{pool: pool, secrets: secrets, jobLease: JobLease}, nil
}

func (p *PostgresRepository) Close() {
	p.pool.Close()
}

// PoolStats reports the live pool counters for the metrics endpoint. It is read
// per scrape, so the numbers describe the pool now rather than a sample taken by
// a background ticker.
func (p *PostgresRepository) PoolStats() *pgxpool.Stat {
	return p.pool.Stat()
}

func (p *PostgresRepository) Ready(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

func (p *PostgresRepository) ListTenants(ctx context.Context, opts ListOptions) ([]domain.Tenant, string, error) {
	query := tenantSelect + ` WHERE TRUE`
	query, args := paginateQuery(query, nil, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Tenant, 0)
	for rows.Next() {
		item, scanErr := scanTenant(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(t domain.Tenant) (time.Time, string) { return t.CreatedAt, t.ID })
	return items, next, nil
}

func (p *PostgresRepository) GetTenant(ctx context.Context, tenantID string) (domain.Tenant, error) {
	return scanTenant(p.pool.QueryRow(ctx, tenantSelect+` WHERE id=$1`, tenantID))
}

func (p *PostgresRepository) GetTenantBySlug(ctx context.Context, slug string) (domain.Tenant, error) {
	return scanTenant(p.pool.QueryRow(ctx, tenantSelect+` WHERE lower(slug)=lower($1)`, slug))
}

func (p *PostgresRepository) CreateTenant(ctx context.Context, tenant domain.Tenant) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO tenants (id, slug, name, disabled, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		tenant.ID, tenant.Slug, tenant.Name, tenant.Disabled, tenant.CreatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateTenant(ctx context.Context, tenant domain.Tenant) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE tenants SET slug=$2, name=$3, disabled=$4 WHERE id=$1`,
		tenant.ID, tenant.Slug, tenant.Name, tenant.Disabled,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

// CountTenantResources returns per-kind row counts for one tenant. It is used
// to fail a destructive tenant delete closed unless the caller explicitly
// requests a cascade.
func (p *PostgresRepository) CountTenantResources(ctx context.Context, tenantID string) (map[string]int, error) {
	kinds := []struct {
		name  string
		query string
	}{
		{"assets", `SELECT count(*) FROM assets WHERE tenant_id=$1`},
		{"sites", `SELECT count(*) FROM sites WHERE tenant_id=$1`},
		{"users", `SELECT count(*) FROM users WHERE tenant_id=$1`},
		{"evidence", `SELECT count(*) FROM evidence WHERE tenant_id=$1`},
		{"incidents", `SELECT count(*) FROM incidents WHERE tenant_id=$1`},
		{"baselines", `SELECT count(*) FROM baseline_versions WHERE tenant_id=$1`},
		{"jobs", `SELECT count(*) FROM detection_jobs WHERE tenant_id=$1`},
		{"alertChannels", `SELECT count(*) FROM alert_channels WHERE tenant_id=$1`},
		{"auditEvents", `SELECT count(*) FROM audit_events WHERE tenant_id=$1`},
		{"incidentEvents", `SELECT count(*) FROM incident_events WHERE tenant_id=$1`},
	}
	result := make(map[string]int, len(kinds))
	for _, kind := range kinds {
		var count int
		if err := p.pool.QueryRow(ctx, kind.query, tenantID).Scan(&count); err != nil {
			return nil, normalizePostgresError(err)
		}
		result[kind.name] = count
	}
	return result, nil
}

func (p *PostgresRepository) DeleteTenant(ctx context.Context, tenantID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return normalizePostgresError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Children first, respecting foreign keys down to tenants.
	statements := []string{
		`DELETE FROM alert_deliveries WHERE tenant_id=$1`,
		`DELETE FROM alert_channels WHERE tenant_id=$1`,
		`DELETE FROM audit_events WHERE tenant_id=$1`,
		`DELETE FROM incident_events WHERE tenant_id=$1`,
		`DELETE FROM incidents WHERE tenant_id=$1`,
		`DELETE FROM evidence WHERE tenant_id=$1`,
		`DELETE FROM detection_jobs WHERE tenant_id=$1`,
		`DELETE FROM baseline_versions WHERE tenant_id=$1`,
		`DELETE FROM monitor_policies WHERE tenant_id=$1`,
		`DELETE FROM assets WHERE tenant_id=$1`,
		`DELETE FROM sites WHERE tenant_id=$1`,
		`DELETE FROM users WHERE tenant_id=$1`,
		`DELETE FROM tenants WHERE id=$1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, tenantID); err != nil {
			return normalizePostgresError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return normalizePostgresError(err)
	}
	return nil
}

const tenantSelect = `SELECT id, slug, name, disabled, created_at FROM tenants`

func scanTenant(row rowScanner) (domain.Tenant, error) {
	var tenant domain.Tenant
	err := row.Scan(&tenant.ID, &tenant.Slug, &tenant.Name, &tenant.Disabled, &tenant.CreatedAt)
	return tenant, normalizePostgresError(err)
}

func (p *PostgresRepository) CreateUser(ctx context.Context, user domain.User) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, password_hash, role, disabled, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		user.ID, user.TenantID, user.Username, user.PasswordHash, user.Role, user.Disabled, user.CreatedAt,
	)
	return normalizePostgresError(err)
}

const userSelect = `
	SELECT id, tenant_id, username, password_hash, role, disabled, created_at FROM users`

func scanUser(row rowScanner) (domain.User, error) {
	var user domain.User
	err := row.Scan(
		&user.ID, &user.TenantID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt,
	)
	return user, normalizePostgresError(err)
}

func (p *PostgresRepository) FindUserByUsername(ctx context.Context, tenantID, username string) (domain.User, error) {
	return scanUser(p.pool.QueryRow(ctx, userSelect+` WHERE tenant_id=$1 AND lower(username)=lower($2)`, tenantID, username))
}

func (p *PostgresRepository) GetUser(ctx context.Context, tenantID, id string) (domain.User, error) {
	return scanUser(p.pool.QueryRow(ctx, userSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListUsers(ctx context.Context, tenantID string) ([]domain.User, error) {
	rows, err := p.pool.Query(ctx, userSelect+` WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.User, 0)
	for rows.Next() {
		item, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) UpdateUser(ctx context.Context, user domain.User) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE users SET username=$3, password_hash=$4, role=$5, disabled=$6
		WHERE tenant_id=$1 AND id=$2`,
		user.TenantID, user.ID, user.Username, user.PasswordHash, user.Role, user.Disabled,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) CreateAsset(ctx context.Context, asset domain.Asset) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO assets (
			id, tenant_id, site_id, name, url, asset_type, owner_id, enabled,
			verification_status, verification_method, verification_token, verification_token_hash, verification_issued_at, verification_expires_at, created_at, updated_at
		) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), NULLIF($12, ''), $13, $14, $15, $16)`,
		asset.ID, asset.TenantID, asset.SiteID, asset.Name, asset.URL, asset.Type, asset.OwnerID,
		asset.Enabled, asset.VerificationStatus, asset.VerificationMethod, asset.VerificationToken, asset.VerificationTokenHash, asset.VerificationIssuedAt, asset.VerificationExpiresAt,
		asset.CreatedAt, asset.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateAsset(ctx context.Context, asset domain.Asset) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE assets SET name=$3, owner_id=$4, enabled=$5, verification_status=$6,
			verification_token=NULLIF($7, ''), verification_token_hash=NULLIF($8, ''),
			verification_issued_at=$9, verification_expires_at=$10, site_id=NULLIF($11, '')::uuid, updated_at=$12
		WHERE tenant_id=$1 AND id=$2`,
		asset.TenantID, asset.ID, asset.Name, asset.OwnerID, asset.Enabled,
		asset.VerificationStatus, asset.VerificationToken, asset.VerificationTokenHash, asset.VerificationIssuedAt, asset.VerificationExpiresAt, asset.SiteID, asset.UpdatedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetAsset(ctx context.Context, tenantID, id string) (domain.Asset, error) {
	row := p.pool.QueryRow(ctx, assetSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	return scanAsset(row)
}

func (p *PostgresRepository) ListAssets(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Asset, string, error) {
	args := []any{tenantID}
	query := assetSelect + ` WHERE tenant_id=$1`
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Asset, 0)
	for rows.Next() {
		item, scanErr := scanAsset(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(a domain.Asset) (time.Time, string) { return a.CreatedAt, a.ID })
	return items, next, nil
}

const assetSelect = `
	SELECT id, tenant_id, COALESCE(site_id::text, ''), name, url, asset_type, owner_id,
		enabled, verification_status, verification_method, COALESCE(verification_token, ''), COALESCE(verification_token_hash, ''),
		verification_issued_at, verification_expires_at, created_at, updated_at FROM assets`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAsset(row rowScanner) (domain.Asset, error) {
	var asset domain.Asset
	err := row.Scan(
		&asset.ID, &asset.TenantID, &asset.SiteID, &asset.Name, &asset.URL, &asset.Type,
		&asset.OwnerID, &asset.Enabled, &asset.VerificationStatus, &asset.VerificationMethod, &asset.VerificationToken, &asset.VerificationTokenHash,
		&asset.VerificationIssuedAt, &asset.VerificationExpiresAt, &asset.CreatedAt, &asset.UpdatedAt,
	)
	return asset, normalizePostgresError(err)
}

// CountAssets answers the dashboard's registry totals in one scan instead of
// paging every asset row into the control plane.
func (p *PostgresRepository) CountAssets(ctx context.Context, tenantID string) (int, int, error) {
	var total, verified int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE verification_status='VERIFIED')
		FROM assets WHERE tenant_id=$1`, tenantID).Scan(&total, &verified)
	if err != nil {
		return 0, 0, normalizePostgresError(err)
	}
	return total, verified, nil
}

func (p *PostgresRepository) CreateSite(ctx context.Context, site domain.Site) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO sites (id, tenant_id, name, domain, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		site.ID, site.TenantID, site.Name, site.Domain, site.OwnerID, site.CreatedAt, site.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateSite(ctx context.Context, site domain.Site) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE sites SET name=$3, domain=$4, owner_id=$5, updated_at=$6
		WHERE tenant_id=$1 AND id=$2`,
		site.TenantID, site.ID, site.Name, site.Domain, site.OwnerID, site.UpdatedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetSite(ctx context.Context, tenantID, id string) (domain.Site, error) {
	return scanSite(p.pool.QueryRow(ctx, siteSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListSites(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Site, string, error) {
	args := []any{tenantID}
	query := siteSelect + ` WHERE tenant_id=$1`
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Site, 0)
	for rows.Next() {
		item, scanErr := scanSite(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(s domain.Site) (time.Time, string) { return s.CreatedAt, s.ID })
	return items, next, nil
}

// CountAssetsBySite counts the assets filed under a site. Nothing cascades
// site deletion, so this is what lets the delete fail with a count the caller
// can act on rather than surfacing a raw foreign-key violation.
func (p *PostgresRepository) CountAssetsBySite(ctx context.Context, tenantID, siteID string) (int, error) {
	var count int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM assets WHERE tenant_id=$1 AND site_id=$2`, tenantID, siteID).Scan(&count)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return count, nil
}

// DeleteSite removes a site the tenant owns. The tagged-tenant lookup means a
// cross-tenant id is ErrNotFound rather than a silent no-op, matching every
// other resource read. The assets foreign key is the backstop: if a row
// references this site the delete is refused here without inspecting the
// database error, because a partitioned check would still race an insert.
func (p *PostgresRepository) DeleteSite(ctx context.Context, tenantID, id string) error {
	var referenced int
	if err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM assets WHERE tenant_id=$1 AND site_id=$2`, tenantID, id).Scan(&referenced); err != nil {
		return normalizePostgresError(err)
	}
	if referenced > 0 {
		return ErrConflict
	}
	tag, err := p.pool.Exec(ctx,
		`DELETE FROM sites WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const siteSelect = `SELECT id, tenant_id, name, domain, owner_id, created_at, updated_at FROM sites`

func scanSite(row rowScanner) (domain.Site, error) {
	var site domain.Site
	err := row.Scan(&site.ID, &site.TenantID, &site.Name, &site.Domain, &site.OwnerID, &site.CreatedAt, &site.UpdatedAt)
	return site, normalizePostgresError(err)
}

func (p *PostgresRepository) UpsertPolicy(ctx context.Context, policy domain.MonitorPolicy) error {
	config, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO monitor_policies (id, tenant_id, asset_id, version, config, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, asset_id) DO UPDATE
		SET version=EXCLUDED.version, config=EXCLUDED.config, updated_at=EXCLUDED.updated_at`,
		policy.ID, policy.TenantID, policy.AssetID, policy.Version, config, policy.CreatedAt, policy.UpdatedAt,
	)
	return normalizePostgresError(err)
}

// RewrapPolicy stores one policy's config with its credentials re-sealed. The
// whole config is rewritten because the credentials live inside the jsonb blob,
// but the caller has already read the row and only replaced the ciphertext
// fields, so nothing else can differ.
func (p *PostgresRepository) RewrapPolicy(ctx context.Context, policy domain.MonitorPolicy) error {
	config, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE monitor_policies SET config=$3
		WHERE tenant_id=$1 AND asset_id=$2`,
		policy.TenantID, policy.AssetID, config,
	)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresRepository) GetPolicy(ctx context.Context, tenantID, assetID string) (domain.MonitorPolicy, error) {
	var raw []byte
	err := p.pool.QueryRow(ctx, `
		SELECT config FROM monitor_policies WHERE tenant_id=$1 AND asset_id=$2`, tenantID, assetID,
	).Scan(&raw)
	if err != nil {
		return domain.MonitorPolicy{}, normalizePostgresError(err)
	}
	var policy domain.MonitorPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return domain.MonitorPolicy{}, err
	}
	return policy, nil
}

func (p *PostgresRepository) ListPolicies(ctx context.Context, tenantID string, opts ListOptions) ([]domain.MonitorPolicy, string, error) {
	args := []any{tenantID}
	query := `SELECT config FROM monitor_policies WHERE tenant_id=$1`
	query, args = paginateQuery(query, args, opts, "updated_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.MonitorPolicy, 0)
	for rows.Next() {
		var raw []byte
		var policy domain.MonitorPolicy
		if err := rows.Scan(&raw); err != nil {
			return nil, "", normalizePostgresError(err)
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			return nil, "", err
		}
		result = append(result, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(p domain.MonitorPolicy) (time.Time, string) { return p.UpdatedAt, p.ID })
	return items, next, nil
}

func (p *PostgresRepository) CreateBaseline(ctx context.Context, baseline domain.BaselineVersion) error {
	snapshot, err := json.Marshal(baseline.Snapshot)
	if err != nil {
		return err
	}
	evidenceIDs, _ := json.Marshal(baseline.EvidenceIDs)
	_, err = p.pool.Exec(ctx, `
		INSERT INTO baseline_versions (
			id,tenant_id,asset_id,version,status,snapshot,evidence_ids,created_by,
			approved_by,created_at,approved_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,$11)`,
		baseline.ID, baseline.TenantID, baseline.AssetID, baseline.Version, baseline.Status,
		snapshot, evidenceIDs, baseline.CreatedBy, baseline.ApprovedBy, baseline.CreatedAt, baseline.ApprovedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateBaseline(ctx context.Context, baseline domain.BaselineVersion) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if baseline.Status == domain.BaselineApproved {
		if _, err := tx.Exec(ctx, `
			UPDATE baseline_versions SET status='RETIRED'
			WHERE tenant_id=$1 AND asset_id=$2 AND id<>$3 AND status='APPROVED'`,
			baseline.TenantID, baseline.AssetID, baseline.ID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE baseline_versions SET status=$3, approved_by=NULLIF($4,'')::uuid, approved_at=$5
		WHERE tenant_id=$1 AND id=$2`,
		baseline.TenantID, baseline.ID, baseline.Status, baseline.ApprovedBy, baseline.ApprovedAt,
	)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (p *PostgresRepository) GetBaseline(ctx context.Context, tenantID, id string) (domain.BaselineVersion, error) {
	return scanBaseline(p.pool.QueryRow(ctx, baselineSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) GetApprovedBaseline(ctx context.Context, tenantID, assetID string) (domain.BaselineVersion, error) {
	return scanBaseline(p.pool.QueryRow(ctx, baselineSelect+`
		WHERE tenant_id=$1 AND asset_id=$2 AND status='APPROVED' ORDER BY version DESC LIMIT 1`,
		tenantID, assetID))
}

func (p *PostgresRepository) ListBaselines(ctx context.Context, tenantID, assetID string, opts ListOptions) ([]domain.BaselineVersion, string, error) {
	args := []any{tenantID}
	query := baselineSelect + ` WHERE tenant_id=$1`
	if assetID != "" {
		query += ` AND asset_id=$2`
		args = append(args, assetID)
	}
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.BaselineVersion, 0)
	for rows.Next() {
		item, scanErr := scanBaseline(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(b domain.BaselineVersion) (time.Time, string) { return b.CreatedAt, b.ID })
	return items, next, nil
}

const baselineSelect = `
	SELECT id,tenant_id,asset_id,version,status,snapshot,evidence_ids,created_by,
		COALESCE(approved_by::text,''),created_at,approved_at FROM baseline_versions`

func scanBaseline(row rowScanner) (domain.BaselineVersion, error) {
	var baseline domain.BaselineVersion
	var snapshot, evidenceIDs []byte
	err := row.Scan(
		&baseline.ID, &baseline.TenantID, &baseline.AssetID, &baseline.Version, &baseline.Status,
		&snapshot, &evidenceIDs, &baseline.CreatedBy, &baseline.ApprovedBy,
		&baseline.CreatedAt, &baseline.ApprovedAt,
	)
	if err != nil {
		return baseline, normalizePostgresError(err)
	}
	if err := json.Unmarshal(snapshot, &baseline.Snapshot); err != nil {
		return baseline, err
	}
	if err := json.Unmarshal(evidenceIDs, &baseline.EvidenceIDs); err != nil {
		return baseline, err
	}
	return baseline, nil
}

// activeJobConstraint is the partial unique index created by migration 000012.
// It allows only one QUEUED or RUNNING job per (tenant, asset, mode).
const activeJobConstraint = "detection_jobs_active_uidx"

func (p *PostgresRepository) CreateJob(ctx context.Context, job domain.DetectionJob) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO detection_jobs (
			id,tenant_id,asset_id,mode,status,policy_version,baseline_version,error_code,
			error_message,retry_count,max_attempts,created_at,started_at,finished_at
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,0),NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14)`,
		job.ID, job.TenantID, job.AssetID, job.Mode, job.Status, job.PolicyVersion,
		job.BaselineVersion, job.ErrorCode, job.ErrorMessage, job.RetryCount, job.MaxAttempts, job.CreatedAt, job.StartedAt, job.FinishedAt,
	)
	// A conflict on the active-job index is an idempotent hit, not a failure, so
	// it must be distinguished from the other unique violations that
	// normalizePostgresError collapses into ErrConflict.
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" &&
		postgresError.ConstraintName == activeJobConstraint {
		return ErrJobAlreadyActive
	}
	return normalizePostgresError(err)
}

// JobLease reports the lease this repository enforces on a RUNNING claim.
func (p *PostgresRepository) JobLease() time.Duration {
	return p.jobLease
}

// SetJobLease replaces the default lease. A non-positive value is ignored so a
// misread environment variable cannot make every claim immediately reclaimable.
func (p *PostgresRepository) SetJobLease(lease time.Duration) {
	if lease > 0 {
		p.jobLease = lease
	}
}

func (p *PostgresRepository) ClaimJob(ctx context.Context, tenantID, id string, startedAt time.Time) (bool, error) {
	leaseCutoff := startedAt.Add(-p.jobLease)
	tag, err := p.pool.Exec(ctx, `
		UPDATE detection_jobs SET status='RUNNING', started_at=$3
		WHERE tenant_id=$1 AND id=$2
			AND (
				status='QUEUED'
				OR (status='RUNNING' AND started_at < $4)
			)`, tenantID, id, startedAt, leaseCutoff)
	if err != nil {
		return false, normalizePostgresError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *PostgresRepository) UpdateJob(ctx context.Context, job domain.DetectionJob) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE detection_jobs SET status=$3,baseline_version=NULLIF($4,0),error_code=NULLIF($5,''),
			error_message=NULLIF($6,''),retry_count=$7,max_attempts=$8,started_at=$9,finished_at=$10
		WHERE tenant_id=$1 AND id=$2`,
		job.TenantID, job.ID, job.Status, job.BaselineVersion, job.ErrorCode,
		job.ErrorMessage, job.RetryCount, job.MaxAttempts, job.StartedAt, job.FinishedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetJob(ctx context.Context, tenantID, id string) (domain.DetectionJob, error) {
	return scanJob(p.pool.QueryRow(ctx, jobSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListJobs(ctx context.Context, tenantID string, opts ListOptions) ([]domain.DetectionJob, string, error) {
	args := []any{tenantID}
	query := jobSelect + ` WHERE tenant_id=$1`
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.DetectionJob, 0)
	for rows.Next() {
		item, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(j domain.DetectionJob) (time.Time, string) { return j.CreatedAt, j.ID })
	return items, next, nil
}

func (p *PostgresRepository) CancelJob(ctx context.Context, tenantID, id string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE detection_jobs SET status='CANCELLED', finished_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status IN ('QUEUED','RUNNING')`,
		tenantID, id)
	if err != nil {
		return false, normalizePostgresError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *PostgresRepository) ReapTimedOut(ctx context.Context, tenantID string, before time.Time) (int, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE detection_jobs SET status='TIMED_OUT', finished_at=now(), error_code='CHECK_TIMEOUT'
		WHERE tenant_id=$1 AND status='RUNNING' AND started_at < $2`,
		tenantID, before)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return int(tag.RowsAffected()), nil
}

// JobSchedulingState collapses the tenant's job history into one row per
// (asset, mode). MAX(created_at) feeds the due-interval check and
// BOOL_OR(status IN ('QUEUED','RUNNING')) is the active-slot check; both
// aggregate in the database, and the (tenant_id, asset_id, mode, created_at)
// index from migration 000001 covers the GROUP BY without touching the table.
func (p *PostgresRepository) JobSchedulingState(ctx context.Context, tenantID string) ([]JobActivity, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT asset_id, mode, MAX(created_at) AS latest,
			BOOL_OR(status IN ('QUEUED','RUNNING')) AS active
		FROM detection_jobs
		WHERE tenant_id=$1
		GROUP BY asset_id, mode`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]JobActivity, 0)
	for rows.Next() {
		var item JobActivity
		if err := rows.Scan(&item.AssetID, &item.Mode, &item.LatestCreatedAt, &item.HasActive); err != nil {
			return nil, normalizePostgresError(err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

// DeleteJobsBefore applies job retention. Only terminal rows are eligible:
// QUEUED is work not yet started, and RUNNING is crash-recovery state that
// ClaimJob reclaims, so touching either would destroy live scheduling state.
func (p *PostgresRepository) DeleteJobsBefore(ctx context.Context, tenantID string, before time.Time) (int, error) {
	tag, err := p.pool.Exec(ctx, `
		DELETE FROM detection_jobs
		WHERE tenant_id=$1 AND created_at < $2 AND status NOT IN ('QUEUED','RUNNING')`,
		tenantID, before)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return int(tag.RowsAffected()), nil
}

// DetectionHealth is the platform-wide liveness read behind the self-health
// metrics. The maximum is deliberately unbounded by the window: an install that
// stopped monitoring yesterday has to be able to say when it last worked, and a
// windowed maximum would report "never" for exactly the case worth reporting.
// The partial index in migration 000017 keeps both halves off a sequential scan.
func (p *PostgresRepository) DetectionHealth(ctx context.Context, since time.Time) (DetectionHealth, error) {
	var health DetectionHealth
	err := p.pool.QueryRow(ctx, `
		SELECT
			(SELECT max(finished_at) FROM detection_jobs WHERE status='SUCCEEDED'),
			count(*) FILTER (WHERE status='SUCCEEDED'),
			count(*) FILTER (WHERE status IN ('FAILED','TIMED_OUT'))
		FROM detection_jobs WHERE finished_at >= $1`, since).Scan(
		&health.LastSucceededAt, &health.Succeeded, &health.Failed)
	if err != nil {
		return DetectionHealth{}, normalizePostgresError(err)
	}
	return health, nil
}

// ConsecutiveFailedChecks counts the trailing run of failures for one
// (asset, mode). Cancelled checks are left out of the scan entirely rather than
// treated as a break: an operator cancelling a job says nothing about whether
// the asset can be monitored, so it must neither extend the run nor end it.
// detection_jobs_schedule_idx (tenant_id, asset_id, mode, created_at DESC) is
// the exact ordering this reads in.
func (p *PostgresRepository) ConsecutiveFailedChecks(ctx context.Context, tenantID, assetID, mode string, window int) (int, error) {
	if window <= 0 {
		return 0, nil
	}
	rows, err := p.pool.Query(ctx, `
		SELECT status FROM detection_jobs
		WHERE tenant_id=$1 AND asset_id=$2 AND mode=$3
			AND status IN ('SUCCEEDED','FAILED','TIMED_OUT')
		ORDER BY created_at DESC
		LIMIT $4`, tenantID, assetID, mode, window)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	defer rows.Close()
	streak := 0
	for rows.Next() {
		var status domain.JobStatus
		if err := rows.Scan(&status); err != nil {
			return 0, err
		}
		if status == domain.JobSucceeded {
			break
		}
		streak++
	}
	if err := rows.Err(); err != nil {
		return 0, normalizePostgresError(err)
	}
	return streak, nil
}

const jobSelect = `
	SELECT id,tenant_id,asset_id,mode,status,policy_version,COALESCE(baseline_version,0),
		COALESCE(error_code,''),COALESCE(error_message,''),retry_count,max_attempts,created_at,started_at,finished_at
	FROM detection_jobs`

func scanJob(row rowScanner) (domain.DetectionJob, error) {
	var job domain.DetectionJob
	err := row.Scan(
		&job.ID, &job.TenantID, &job.AssetID, &job.Mode, &job.Status, &job.PolicyVersion,
		&job.BaselineVersion, &job.ErrorCode, &job.ErrorMessage, &job.RetryCount, &job.MaxAttempts, &job.CreatedAt,
		&job.StartedAt, &job.FinishedAt,
	)
	return job, normalizePostgresError(err)
}

func (p *PostgresRepository) CreateEvidence(ctx context.Context, item domain.Evidence) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO evidence (
			id,tenant_id,asset_id,job_id,kind,media_type,size_bytes,sha256,object_key,created_at,retain_until
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		item.ID, item.TenantID, item.AssetID, item.JobID, item.Kind, item.MediaType,
		item.Size, item.SHA256, item.ObjectKey, item.CreatedAt, item.RetainUntil,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetEvidence(ctx context.Context, tenantID, id string) (domain.Evidence, error) {
	return scanEvidence(p.pool.QueryRow(ctx, evidenceSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListEvidence(ctx context.Context, tenantID, assetID string, opts ListOptions) ([]domain.Evidence, string, error) {
	args := []any{tenantID}
	query := evidenceSelect + ` WHERE tenant_id=$1`
	if assetID != "" {
		query += ` AND asset_id=$2`
		args = append(args, assetID)
	}
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Evidence, 0)
	for rows.Next() {
		item, scanErr := scanEvidence(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(e domain.Evidence) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
}

func (p *PostgresRepository) ListExpiredEvidence(ctx context.Context, tenantID string, before time.Time) ([]domain.Evidence, error) {
	rows, err := p.pool.Query(ctx, evidenceSelect+`
		WHERE tenant_id=$1 AND retain_until IS NOT NULL AND retain_until <= $2
		ORDER BY created_at ASC`, tenantID, before)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Evidence, 0)
	for rows.Next() {
		item, scanErr := scanEvidence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) IsEvidenceReferenced(ctx context.Context, tenantID, id string) (bool, error) {
	var referenced bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM incidents i
			WHERE i.tenant_id=$1
			AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(i.evidence_ids) evidence_id
				WHERE evidence_id=$2
			)
		)`, tenantID, id).Scan(&referenced)
	return referenced, normalizePostgresError(err)
}

func (p *PostgresRepository) DeleteEvidence(ctx context.Context, tenantID, id string) error {
	tag, err := p.pool.Exec(ctx, `
		DELETE FROM evidence e
		WHERE e.tenant_id=$1 AND e.id=$2
		AND NOT EXISTS (
			SELECT 1 FROM incidents i
			WHERE i.tenant_id=e.tenant_id
			AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(i.evidence_ids) evidence_id
				WHERE evidence_id=e.id::text
			)
		)`, tenantID, id)
	if err == nil && tag.RowsAffected() == 0 {
		var exists bool
		if queryErr := p.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM evidence WHERE tenant_id=$1 AND id=$2)`, tenantID, id).Scan(&exists); queryErr != nil {
			return normalizePostgresError(queryErr)
		} else if exists {
			return ErrConflict
		}
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

const evidenceSelect = `
	SELECT id,tenant_id,asset_id,job_id,kind,media_type,size_bytes,sha256,object_key,
		created_at,retain_until FROM evidence`

func scanEvidence(row rowScanner) (domain.Evidence, error) {
	var item domain.Evidence
	err := row.Scan(
		&item.ID, &item.TenantID, &item.AssetID, &item.JobID, &item.Kind, &item.MediaType,
		&item.Size, &item.SHA256, &item.ObjectKey, &item.CreatedAt, &item.RetainUntil,
	)
	return item, normalizePostgresError(err)
}

// IncidentStatusCounts feeds both the dashboard summary and the incident
// gauges, so those numbers reflect the whole table rather than whichever page
// a list endpoint happened to return.
func (p *PostgresRepository) IncidentStatusCounts(ctx context.Context, tenantID string) (int, int, int, error) {
	var open, highRisk, critical int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE')),
			count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE') AND severity IN ('HIGH','CRITICAL')),
			count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE') AND severity='CRITICAL')
		FROM incidents WHERE tenant_id=$1`, tenantID).Scan(&open, &highRisk, &critical)
	if err != nil {
		return 0, 0, 0, normalizePostgresError(err)
	}
	return open, highRisk, critical, nil
}

// CountOpenIncidents is the platform-wide variant of IncidentStatusCounts:
// the /metrics scrape carries no tenant context, so the gauges count across
// every tenant.
func (p *PostgresRepository) CountOpenIncidents(ctx context.Context) (int, int, int, error) {
	var open, highRisk, critical int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE')),
			count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE') AND severity IN ('HIGH','CRITICAL')),
			count(*) FILTER (WHERE status NOT IN ('CLOSED','FALSE_POSITIVE') AND severity='CRITICAL')
		FROM incidents`).Scan(&open, &highRisk, &critical)
	if err != nil {
		return 0, 0, 0, normalizePostgresError(err)
	}
	return open, highRisk, critical, nil
}

// FailedJobCount counts terminal failures for the dashboard and report
// summaries without reading job rows at all.
func (p *PostgresRepository) FailedJobCount(ctx context.Context, tenantID string) (int, error) {
	var count int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FROM detection_jobs
		WHERE tenant_id=$1 AND status IN ('FAILED','TIMED_OUT')`, tenantID).Scan(&count)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return count, nil
}

func (p *PostgresRepository) UpsertIncident(ctx context.Context, incident domain.Incident) (domain.Incident, IncidentUpsertOutcome, error) {
	signals, _ := json.Marshal(incident.Signals)
	evidenceIDs, _ := json.Marshal(incident.EvidenceIDs)
	// The timeline entry is written by the same statement as the counter it
	// describes. A second write could fail, or the process could die between the
	// two, and the occurrence this table exists to preserve would be lost again
	// -- which is precisely the bug being fixed here. A data-modifying CTE always
	// runs to completion even though the final SELECT does not read it.
	//
	// `(xmax = 0)` in the RETURNING must stay as it is: it is what tells the
	// caller whether this round created the incident or merged into one, and the
	// broker event name and alert dispatch depend on it.
	//
	// `prior` exists to answer one question the upsert itself cannot: was the row
	// we just landed on CLOSED or FALSE_POSITIVE before this statement? It has
	// to be read by a sibling CTE rather than from the RETURNING, because that
	// RETURNING sees the row *after* DO UPDATE has rewritten status, so
	// `incidents.status` there is already OPEN and every reopen would be
	// mislabelled RECURRED. A CTE without a data-modifying sibling reads the
	// pre-statement snapshot, which is the value we need. All three CTEs share
	// one snapshot, so `prior` and the INSERT below cannot disagree about what
	// the row looked like on entry.
	row := p.pool.QueryRow(ctx, `
		WITH prior AS (
			SELECT status FROM incidents
			WHERE tenant_id=$2 AND asset_id=$3 AND baseline_version=$5 AND fingerprint=$6
		), upserted AS (
			INSERT INTO incidents (
				id,tenant_id,asset_id,baseline_id,baseline_version,fingerprint,severity,status,title,
				signals,evidence_ids,occurrence_count,owner_id,resolution,created_at,first_seen_at,last_seen_at,acknowledged_at,resolved_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid,NULLIF($14,''),$15,$16,$17,$18,$19)
			ON CONFLICT (tenant_id,asset_id,baseline_version,fingerprint)
			DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at,
				occurrence_count=incidents.occurrence_count+1,
				-- A recurrence on a closed incident reopens it. The partial index
				-- this replaces simply refused to match those rows, so the
				-- recurrence opened a second incident with the same fingerprint
				-- and the operator saw two unrelated rows for one problem. The
				-- incident keeps its id, so its timeline and its evidence stay
				-- attached to it.
				status=CASE WHEN incidents.status IN ('CLOSED','FALSE_POSITIVE')
					THEN 'OPEN'::incident_status ELSE incidents.status END,
				-- Both of the merged fields below must match the in-memory
				-- implementation exactly. They did not: severity was allowed to
				-- downgrade between non-CRITICAL levels here while memory kept it
				-- monotonic, and the jsonb concatenation kept duplicate evidence
				-- ids that memory deduplicated. Same input, two different
				-- incident rows. GREATEST works because a PostgreSQL enum
				-- compares by declaration order, and the DISTINCT aggregation
				-- makes the merged list deterministic so the two stores agree on
				-- content and order.
				signals=EXCLUDED.signals,
				evidence_ids=(
					SELECT COALESCE(jsonb_agg(DISTINCT value ORDER BY value), '[]'::jsonb)
					FROM jsonb_array_elements(incidents.evidence_ids || EXCLUDED.evidence_ids) AS merged(value)
				),
				severity=GREATEST(incidents.severity, EXCLUDED.severity)
			RETURNING id, (xmax = 0) AS created, severity,
				CASE WHEN (xmax = 0) THEN 'CREATED'
				     WHEN (SELECT status FROM prior) IN ('CLOSED','FALSE_POSITIVE') THEN 'REOPENED'
				     ELSE 'RECURRED' END AS event_type
		), logged AS (
			INSERT INTO incident_events (
				id,tenant_id,incident_id,event_type,severity,title,signals,evidence_ids,job_id,created_at
			)
			SELECT $20,$2,upserted.id,
				upserted.event_type::incident_event_type,
				upserted.severity,$9,$10,$11,NULLIF($21,'')::uuid,$15
			FROM upserted
			RETURNING id
		)
		SELECT id, event_type FROM upserted`,
		incident.ID, incident.TenantID, incident.AssetID, incident.BaselineID, incident.BaselineVersion,
		incident.Fingerprint, incident.Severity, incident.Status, incident.Title, signals, evidenceIDs,
		incident.OccurrenceCount, incident.OwnerID, incident.Resolution, incident.CreatedAt,
		incident.FirstSeenAt, incident.LastSeenAt, incident.AcknowledgedAt, incident.ResolvedAt,
		identity.New(), incident.JobID,
	)
	var mergedID string
	// The statement decides the outcome, not this side: it is the only place
	// that can still see the pre-update status.
	var eventType string
	if err := row.Scan(&mergedID, &eventType); err != nil {
		return domain.Incident{}, IncidentCreated, normalizePostgresError(err)
	}
	outcome := IncidentRecurred
	switch eventType {
	case string(domain.IncidentEventCreated):
		outcome = IncidentCreated
	case string(domain.IncidentEventReopened):
		outcome = IncidentReopened
	}
	// RETURNING gives the real row, so this no longer needs the full-table scan
	// the previous implementation used to find which incident the round merged
	// into.
	merged, err := p.GetIncident(ctx, incident.TenantID, mergedID)
	if err != nil {
		// The write already committed; report the caller's view rather than
		// failing a detection over a read-back that should not fail.
		return incident, outcome, nil
	}
	return merged, outcome, nil
}

func (p *PostgresRepository) UpdateIncident(ctx context.Context, incident domain.Incident) error {
	signals, _ := json.Marshal(incident.Signals)
	evidenceIDs, _ := json.Marshal(incident.EvidenceIDs)
	tag, err := p.pool.Exec(ctx, `
		UPDATE incidents SET severity=$3,status=$4,title=$5,signals=$6,evidence_ids=$7,
			occurrence_count=$8,owner_id=NULLIF($9,'')::uuid,resolution=NULLIF($10,''),
			last_seen_at=$11,acknowledged_at=$12,resolved_at=$13
		WHERE tenant_id=$1 AND id=$2`,
		incident.TenantID, incident.ID, incident.Severity, incident.Status, incident.Title,
		signals, evidenceIDs, incident.OccurrenceCount, incident.OwnerID, incident.Resolution,
		incident.LastSeenAt, incident.AcknowledgedAt, incident.ResolvedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetIncident(ctx context.Context, tenantID, id string) (domain.Incident, error) {
	return scanIncident(p.pool.QueryRow(ctx, incidentSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListIncidents(ctx context.Context, tenantID string, opts ListOptions) ([]domain.Incident, string, error) {
	args := []any{tenantID}
	query := incidentSelect + ` WHERE tenant_id=$1`
	query, args = paginateQuery(query, args, opts, "last_seen_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Incident, 0)
	for rows.Next() {
		item, scanErr := scanIncident(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(i domain.Incident) (time.Time, string) { return i.LastSeenAt, i.ID })
	return items, next, nil
}

const incidentSelect = `
	SELECT id,tenant_id,asset_id,baseline_id,baseline_version,fingerprint,severity,status,title,
		signals,evidence_ids,occurrence_count,COALESCE(owner_id::text,''),COALESCE(resolution,''),
		created_at,first_seen_at,last_seen_at,acknowledged_at,resolved_at FROM incidents`

func scanIncident(row rowScanner) (domain.Incident, error) {
	var incident domain.Incident
	var signals, evidenceIDs []byte
	err := row.Scan(
		&incident.ID, &incident.TenantID, &incident.AssetID, &incident.BaselineID,
		&incident.BaselineVersion, &incident.Fingerprint, &incident.Severity, &incident.Status,
		&incident.Title, &signals, &evidenceIDs, &incident.OccurrenceCount, &incident.OwnerID,
		&incident.Resolution, &incident.CreatedAt, &incident.FirstSeenAt, &incident.LastSeenAt,
		&incident.AcknowledgedAt, &incident.ResolvedAt,
	)
	if err != nil {
		return incident, normalizePostgresError(err)
	}
	if err := json.Unmarshal(signals, &incident.Signals); err != nil {
		return incident, err
	}
	if err := json.Unmarshal(evidenceIDs, &incident.EvidenceIDs); err != nil {
		return incident, err
	}
	return incident, nil
}

func (p *PostgresRepository) AppendIncidentEvent(ctx context.Context, event domain.IncidentEvent) error {
	signals, _ := json.Marshal(event.Signals)
	evidenceIDs, _ := json.Marshal(event.EvidenceIDs)
	details, _ := json.Marshal(event.Details)
	// INSERT ... SELECT rather than VALUES: an entry for an incident that does
	// not exist (or belongs to another tenant) inserts nothing, and the caller
	// gets ErrNotFound. Relying on the foreign key instead would surface as a
	// raw 23503, which normalizePostgresError does not map and which the
	// in-memory store could not reproduce.
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO incident_events (
			id,tenant_id,incident_id,event_type,actor_id,from_status,to_status,severity,title,
			signals,evidence_ids,job_id,details,created_at
		)
		SELECT $1,$2,i.id,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::incident_status,
			NULLIF($7,'')::incident_status,NULLIF($8,'')::severity,NULLIF($9,''),
			$10,$11,NULLIF($12,'')::uuid,$13,$14
		FROM incidents i WHERE i.tenant_id=$2 AND i.id=$3`,
		event.ID, event.TenantID, event.IncidentID, event.Type, event.ActorID,
		event.FromStatus, event.ToStatus, event.Severity, event.Title,
		signals, evidenceIDs, event.JobID, details, event.CreatedAt,
	)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresRepository) ListIncidentEvents(ctx context.Context, tenantID, incidentID string, opts ListOptions) ([]domain.IncidentEvent, string, error) {
	args := []any{tenantID, incidentID}
	query := incidentEventSelect + ` WHERE tenant_id=$1 AND incident_id=$2`
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.IncidentEvent, 0)
	for rows.Next() {
		item, scanErr := scanIncidentEvent(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(e domain.IncidentEvent) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
}

const incidentEventSelect = `
	SELECT id,tenant_id,incident_id,event_type,COALESCE(actor_id::text,''),
		COALESCE(from_status::text,''),COALESCE(to_status::text,''),COALESCE(severity::text,''),
		COALESCE(title,''),signals,evidence_ids,COALESCE(job_id::text,''),
		COALESCE(details,'{}'::jsonb),created_at
	FROM incident_events`

func scanIncidentEvent(row rowScanner) (domain.IncidentEvent, error) {
	var event domain.IncidentEvent
	var signals, evidenceIDs, details []byte
	err := row.Scan(
		&event.ID, &event.TenantID, &event.IncidentID, &event.Type, &event.ActorID,
		&event.FromStatus, &event.ToStatus, &event.Severity, &event.Title,
		&signals, &evidenceIDs, &event.JobID, &details, &event.CreatedAt,
	)
	if err != nil {
		return event, normalizePostgresError(err)
	}
	if err := json.Unmarshal(signals, &event.Signals); err != nil {
		return event, err
	}
	if err := json.Unmarshal(evidenceIDs, &event.EvidenceIDs); err != nil {
		return event, err
	}
	if err := json.Unmarshal(details, &event.Details); err != nil {
		return event, err
	}
	return event, nil
}

// decryptChannelField decrypts one stored channel credential, tolerating only
// the pre-encryption compatibility case.
//
// A value with no version prefix is plaintext from before encryption was
// introduced, and is passed through so the row still works until its next
// update rewrites it. A value that carries a version prefix was encrypted, so a
// failure to decrypt it means the configured keys are wrong. Returning the
// ciphertext in that case -- which is what this used to do -- silently hands a
// bogus URL to the dispatcher and, worse, a bogus HMAC secret to the webhook
// signer, with nothing logged. That is exactly the failure a key rotation would
// produce, so it has to be loud.
func decryptChannelField(secrets security.SecretBox, stored, associatedData string) (string, error) {
	if stored == "" {
		return "", nil
	}
	plaintext, err := secrets.Decrypt(stored, associatedData)
	if err == nil {
		return plaintext, nil
	}
	if _, _, versioned := security.SplitKeyVersion(stored); versioned {
		return "", fmt.Errorf("decrypt alert channel credential: %w", err)
	}
	return stored, nil
}

// channelAssociatedData names the row a channel's ciphertext is bound to. The
// id and tenant are part of it, which is why a channel is updated in place
// rather than recreated: any other id would change the associated data and the
// stored ciphertext would never decrypt again. The definition itself lives in
// the security package, beside the login-credential shape, so the key-rotation
// pass and this store cannot drift apart.
func channelAssociatedData(tenantID, id, field string) string {
	return security.ChannelAssociatedData(tenantID, id, field)
}

func (p *PostgresRepository) CreateAlertChannel(ctx context.Context, channel domain.AlertChannel) error {
	encryptedURL, err := p.secrets.Encrypt(channel.URL, channelAssociatedData(channel.TenantID, channel.ID, "url"))
	if err != nil {
		return err
	}
	encryptedSecret, err := p.secrets.Encrypt(channel.Secret, channelAssociatedData(channel.TenantID, channel.ID, "secret"))
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO alert_channels (
			id,tenant_id,name,channel_type,url,recipients,secret_ciphertext,enabled,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10)`,
		channel.ID, channel.TenantID, channel.Name, channel.Type, encryptedURL, channel.Recipients,
		encryptedSecret, channel.Enabled, channel.CreatedAt, channel.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetAlertChannel(ctx context.Context, tenantID, id string) (domain.AlertChannel, error) {
	var item domain.AlertChannel
	var encryptedURL, encryptedSecret string
	err := p.pool.QueryRow(ctx, `
		SELECT id,tenant_id,name,channel_type,url,recipients,COALESCE(secret_ciphertext,''),enabled,created_at,updated_at
		FROM alert_channels WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&item.ID, &item.TenantID, &item.Name, &item.Type, &encryptedURL, &item.Recipients, &encryptedSecret,
		&item.Enabled, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return domain.AlertChannel{}, normalizePostgresError(err)
	}
	item.URL, err = decryptChannelField(p.secrets, encryptedURL, channelAssociatedData(item.TenantID, item.ID, "url"))
	if err != nil {
		return domain.AlertChannel{}, err
	}
	item.Secret, err = decryptChannelField(p.secrets, encryptedSecret, channelAssociatedData(item.TenantID, item.ID, "secret"))
	if err != nil {
		return domain.AlertChannel{}, err
	}
	return item, nil
}

// RewrapAlertChannel writes both credential columns of one channel back with
// new ciphertext. It exists for the key-rotation pass, and is separate from
// UpdateAlertChannel so a rotation cannot reach any other column: url and
// secret_ciphertext are the only fields a rotation is allowed to change.
//
// Unlike CreateAlertChannel and UpdateAlertChannel it does not seal anything:
// the caller hands it the already re-wrapped ciphertext, matching RewrapPolicy.
func (p *PostgresRepository) RewrapAlertChannel(ctx context.Context, channel domain.AlertChannel) error {
	// The values are written as given: they are already ciphertext, produced by
	// the rotation pass's own keyring, exactly as RewrapPolicy is handed a
	// policy whose credential fields are already re-sealed. Encrypting here as
	// well sealed them twice, and the deployment then read back the inner
	// ciphertext as if it were the URL.
	//
	// updated_at is deliberately not touched. The row's content did not change,
	// only the key that sealed it, and moving the timestamp would make a rotation
	// look like an edit in the channel list.
	tag, err := p.pool.Exec(ctx, `
		UPDATE alert_channels SET url=$3, secret_ciphertext=NULLIF($4,'')
		WHERE tenant_id=$1 AND id=$2`,
		channel.TenantID, channel.ID, channel.URL, channel.Secret,
	)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresRepository) UpdateAlertChannel(ctx context.Context, channel domain.AlertChannel) error {
	encryptedURL, err := p.secrets.Encrypt(channel.URL, channelAssociatedData(channel.TenantID, channel.ID, "url"))
	if err != nil {
		return err
	}
	encryptedSecret, err := p.secrets.Encrypt(channel.Secret, channelAssociatedData(channel.TenantID, channel.ID, "secret"))
	if err != nil {
		return err
	}
	// tenant_id is in the WHERE clause, not just the SET: it is the isolation
	// boundary, and a write that matched on id alone would let one tenant
	// rewrite another's channel.
	tag, err := p.pool.Exec(ctx, `
		UPDATE alert_channels
		SET name=$3,channel_type=$4,url=$5,recipients=$6,secret_ciphertext=NULLIF($7,''),enabled=$8,updated_at=$9
		WHERE tenant_id=$1 AND id=$2`,
		channel.TenantID, channel.ID, channel.Name, channel.Type, encryptedURL, channel.Recipients,
		encryptedSecret, channel.Enabled, channel.UpdatedAt,
	)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresRepository) DeleteAlertChannel(ctx context.Context, tenantID, id string) error {
	tag, err := p.pool.Exec(ctx,
		`DELETE FROM alert_channels WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return normalizePostgresError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PostgresRepository) ListAlertChannels(ctx context.Context, tenantID string) ([]domain.AlertChannel, error) {
	return p.listAlertChannels(ctx, tenantID, false, true)
}

func (p *PostgresRepository) ListAlertChannelsWithSecrets(ctx context.Context, tenantID string) ([]domain.AlertChannel, error) {
	return p.listAlertChannels(ctx, tenantID, true, true)
}

func (p *PostgresRepository) ListAlertChannelsEncrypted(ctx context.Context, tenantID string) ([]domain.AlertChannel, error) {
	return p.listAlertChannels(ctx, tenantID, true, false)
}

// listAlertChannels reads the channel rows. decrypt selects whether the two
// encrypted columns are opened: the API and the dispatcher need plaintext, while
// the rotation pass needs the stored ciphertext -- re-sealing an already
// decrypted value sees no version prefix and reports the row as current.
func (p *PostgresRepository) listAlertChannels(ctx context.Context, tenantID string, includeSecret, decrypt bool) ([]domain.AlertChannel, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id,tenant_id,name,channel_type,url,recipients,COALESCE(secret_ciphertext,''),enabled,created_at,updated_at
		FROM alert_channels WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.AlertChannel, 0)
	for rows.Next() {
		var item domain.AlertChannel
		var encryptedURL, encryptedSecret string
		if err := rows.Scan(
			&item.ID, &item.TenantID, &item.Name, &item.Type, &encryptedURL, &item.Recipients, &encryptedSecret,
			&item.Enabled, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, normalizePostgresError(err)
		}
		if !decrypt {
			item.URL = encryptedURL
			if includeSecret {
				item.Secret = encryptedSecret
			}
			result = append(result, item)
			continue
		}
		item.URL, err = decryptChannelField(p.secrets, encryptedURL, channelAssociatedData(item.TenantID, item.ID, "url"))
		if err != nil {
			return nil, err
		}
		if includeSecret {
			item.Secret, err = decryptChannelField(p.secrets, encryptedSecret, channelAssociatedData(item.TenantID, item.ID, "secret"))
			if err != nil {
				return nil, err
			}
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) CreateAlertDelivery(ctx context.Context, delivery domain.AlertDelivery) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO alert_deliveries (
			id,tenant_id,incident_id,channel_id,dedup_key,status,attempts,last_error,next_attempt_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)`,
		delivery.ID, delivery.TenantID, delivery.IncidentID, delivery.ChannelID, delivery.DedupKey,
		delivery.Status, delivery.Attempts, delivery.LastError, delivery.NextAttemptAt, delivery.CreatedAt, delivery.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateAlertDelivery(ctx context.Context, delivery domain.AlertDelivery) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE alert_deliveries SET status=$3,attempts=$4,last_error=NULLIF($5,''),next_attempt_at=$6,updated_at=$7
		WHERE tenant_id=$1 AND id=$2`, delivery.TenantID, delivery.ID, delivery.Status,
		delivery.Attempts, delivery.LastError, delivery.NextAttemptAt, delivery.UpdatedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ListAlertDeliveries(ctx context.Context, tenantID, incidentID string, opts ListOptions) ([]domain.AlertDelivery, string, error) {
	args := []any{tenantID}
	query := `SELECT id,tenant_id,incident_id,channel_id,dedup_key,status,attempts,COALESCE(last_error,''),next_attempt_at,created_at,updated_at FROM alert_deliveries WHERE tenant_id=$1`
	if incidentID != "" {
		query += ` AND incident_id=$2`
		args = append(args, incidentID)
	}
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.AlertDelivery, 0)
	for rows.Next() {
		var item domain.AlertDelivery
		if err := rows.Scan(&item.ID, &item.TenantID, &item.IncidentID, &item.ChannelID, &item.DedupKey,
			&item.Status, &item.Attempts, &item.LastError, &item.NextAttemptAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, "", normalizePostgresError(err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(d domain.AlertDelivery) (time.Time, string) { return d.CreatedAt, d.ID })
	return items, next, nil
}
func (p *PostgresRepository) AppendAudit(ctx context.Context, event domain.AuditEvent) error {
	details, _ := json.Marshal(event.Details)
	_, err := p.pool.Exec(ctx, `
		INSERT INTO audit_events (
			id,tenant_id,actor_id,action,resource_type,resource_id,result,trace_id,details,created_at
		) VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10)`,
		event.ID, event.TenantID, event.ActorID, event.Action, event.ResourceType,
		event.ResourceID, event.Result, event.TraceID, details, event.CreatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ListAudits(ctx context.Context, tenantID string, filter AuditFilter, opts ListOptions) ([]domain.AuditEvent, string, error) {
	args := []any{tenantID}
	query := `SELECT id,tenant_id,COALESCE(actor_id::text,''),action,resource_type,resource_id,
		result,trace_id,COALESCE(details,'{}'::jsonb),created_at
		FROM audit_events WHERE tenant_id=$1`
	// Every predicate below must be appended before paginateQuery runs: it
	// numbers its cursor and limit placeholders from len(args), so a predicate
	// added afterwards would bind to the wrong parameter.
	//
	// actor_id is cast explicitly because AppendAudit stores it through
	// NULLIF($3,'')::uuid. Callers must reject a malformed value before reaching
	// here; otherwise Postgres raises 22P02 and normalizePostgresError turns a
	// bad filter into a 500.
	if filter.ActorID != "" {
		args = append(args, filter.ActorID)
		query += fmt.Sprintf(" AND actor_id=$%d::uuid", len(args))
	}
	// Exact match, not prefix: the action values are a closed set the writers
	// produce (asset.create, baseline.approved, incident.transition, ...), so a
	// prefix search would be a different feature with a different index.
	if filter.Action != "" {
		args = append(args, filter.Action)
		query += fmt.Sprintf(" AND action=$%d", len(args))
	}
	if filter.ResourceType != "" {
		args = append(args, filter.ResourceType)
		query += fmt.Sprintf(" AND resource_type=$%d", len(args))
	}
	if filter.ResourceID != "" {
		args = append(args, filter.ResourceID)
		query += fmt.Sprintf(" AND resource_id=$%d", len(args))
	}
	if filter.Result != "" {
		args = append(args, filter.Result)
		query += fmt.Sprintf(" AND result=$%d", len(args))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		query += fmt.Sprintf(" AND created_at >= $%d", len(args))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		query += fmt.Sprintf(" AND created_at <= $%d", len(args))
	}
	query, args = paginateQuery(query, args, opts, "created_at")
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.AuditEvent, 0)
	for rows.Next() {
		var item domain.AuditEvent
		var details []byte
		if err := rows.Scan(
			&item.ID, &item.TenantID, &item.ActorID, &item.Action, &item.ResourceType,
			&item.ResourceID, &item.Result, &item.TraceID, &details, &item.CreatedAt,
		); err != nil {
			return nil, "", normalizePostgresError(err)
		}
		if err := json.Unmarshal(details, &item.Details); err != nil {
			return nil, "", err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", normalizePostgresError(err)
	}
	items, next := trimPage(result, opts, func(e domain.AuditEvent) (time.Time, string) { return e.CreatedAt, e.ID })
	return items, next, nil
}

func (p *PostgresRepository) DeleteAuditsBefore(ctx context.Context, tenantID string, before time.Time) (int, error) {
	tag, err := p.pool.Exec(ctx,
		`DELETE FROM audit_events WHERE tenant_id=$1 AND created_at < $2`, tenantID, before)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return int(tag.RowsAffected()), nil
}

// paginateQuery completes a list query that already ends with its WHERE clause:
// it appends the keyset cursor filter, the descending (column, id) ordering and a
// LIMIT of Limit+1 (so the caller can detect a further page). The ordering and
// the LIMIT are emitted together because SQL requires ORDER BY to precede LIMIT;
// a non-positive Limit keeps the result unbounded for internal callers and omits
// only the LIMIT. The cursor selects rows strictly after it in that same order.
func paginateQuery(query string, args []any, opts ListOptions, column string) (string, []any) {
	if opts.Cursor != "" {
		if cursorTime, cursorID, ok := decodeCursor(opts.Cursor); ok {
			next := len(args) + 1
			query += fmt.Sprintf(" AND (%s, id) < ($%d::timestamptz, $%d::uuid)", column, next, next+1)
			args = append(args, cursorTime, cursorID)
		}
	}
	query += fmt.Sprintf(" ORDER BY %s DESC, id DESC", column)
	if opts.Limit > 0 {
		next := len(args) + 1
		query += fmt.Sprintf(" LIMIT $%d", next)
		args = append(args, opts.Limit+1)
	}
	return query, args
}

func normalizePostgresError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrConflict
	}
	return err
}

var _ Repository = (*PostgresRepository)(nil)
var _ = time.Time{}
