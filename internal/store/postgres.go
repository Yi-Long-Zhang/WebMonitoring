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
	"webmonitoring/internal/security"
)

type PostgresRepository struct {
	pool    *pgxpool.Pool
	secrets *security.SecretCipher
}

func NewPostgresRepository(ctx context.Context, dsn, alertSecretKey string) (*PostgresRepository, error) {
	secrets, err := security.NewSecretCipher(alertSecretKey)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &PostgresRepository{pool: pool, secrets: secrets}, nil
}

func (p *PostgresRepository) Close() {
	p.pool.Close()
}

func (p *PostgresRepository) Ready(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

func (p *PostgresRepository) CreateUser(user domain.User) error {
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO users (id, tenant_id, username, password_hash, role, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		user.ID, user.TenantID, user.Username, user.PasswordHash, user.Role, user.CreatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) FindUserByUsername(tenantID, username string) (domain.User, error) {
	var user domain.User
	err := p.pool.QueryRow(context.Background(), `
		SELECT id, tenant_id, username, password_hash, role, created_at
		FROM users WHERE tenant_id = $1 AND lower(username) = lower($2)`,
		tenantID, username,
	).Scan(&user.ID, &user.TenantID, &user.Username, &user.PasswordHash, &user.Role, &user.CreatedAt)
	return user, normalizePostgresError(err)
}

func (p *PostgresRepository) GetUser(tenantID, id string) (domain.User, error) {
	var user domain.User
	err := p.pool.QueryRow(context.Background(), `
		SELECT id, tenant_id, username, password_hash, role, created_at
		FROM users WHERE tenant_id = $1 AND id = $2`, tenantID, id,
	).Scan(&user.ID, &user.TenantID, &user.Username, &user.PasswordHash, &user.Role, &user.CreatedAt)
	return user, normalizePostgresError(err)
}

func (p *PostgresRepository) CreateAsset(asset domain.Asset) error {
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO assets (
			id, tenant_id, site_id, name, url, asset_type, owner_id, enabled,
			verification_status, verification_token, verification_token_hash, verification_issued_at, verification_expires_at, created_at, updated_at
		) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14, $15)`,
		asset.ID, asset.TenantID, asset.SiteID, asset.Name, asset.URL, asset.Type, asset.OwnerID,
		asset.Enabled, asset.VerificationStatus, asset.VerificationToken, asset.VerificationTokenHash, asset.VerificationIssuedAt, asset.VerificationExpiresAt,
		asset.CreatedAt, asset.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateAsset(asset domain.Asset) error {
	tag, err := p.pool.Exec(context.Background(), `
		UPDATE assets SET name=$3, owner_id=$4, enabled=$5, verification_status=$6,
			verification_token=NULLIF($7, ''), verification_token_hash=NULLIF($8, ''),
			verification_issued_at=$9, verification_expires_at=$10, updated_at=$11
		WHERE tenant_id=$1 AND id=$2`,
		asset.TenantID, asset.ID, asset.Name, asset.OwnerID, asset.Enabled,
		asset.VerificationStatus, asset.VerificationToken, asset.VerificationTokenHash, asset.VerificationIssuedAt, asset.VerificationExpiresAt, asset.UpdatedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetAsset(tenantID, id string) (domain.Asset, error) {
	row := p.pool.QueryRow(context.Background(), assetSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	return scanAsset(row)
}

func (p *PostgresRepository) ListAssets(tenantID string) ([]domain.Asset, error) {
	rows, err := p.pool.Query(context.Background(), assetSelect+` WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Asset, 0)
	for rows.Next() {
		item, scanErr := scanAsset(rows)
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

const assetSelect = `
	SELECT id, tenant_id, COALESCE(site_id::text, ''), name, url, asset_type, owner_id,
		enabled, verification_status, COALESCE(verification_token, ''), COALESCE(verification_token_hash, ''),
		verification_issued_at, verification_expires_at, created_at, updated_at FROM assets`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAsset(row rowScanner) (domain.Asset, error) {
	var asset domain.Asset
	err := row.Scan(
		&asset.ID, &asset.TenantID, &asset.SiteID, &asset.Name, &asset.URL, &asset.Type,
		&asset.OwnerID, &asset.Enabled, &asset.VerificationStatus, &asset.VerificationToken, &asset.VerificationTokenHash,
		&asset.VerificationIssuedAt, &asset.VerificationExpiresAt, &asset.CreatedAt, &asset.UpdatedAt,
	)
	return asset, normalizePostgresError(err)
}

func (p *PostgresRepository) UpsertPolicy(policy domain.MonitorPolicy) error {
	config, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), `
		INSERT INTO monitor_policies (id, tenant_id, asset_id, version, config, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, asset_id) DO UPDATE
		SET version=EXCLUDED.version, config=EXCLUDED.config, updated_at=EXCLUDED.updated_at`,
		policy.ID, policy.TenantID, policy.AssetID, policy.Version, config, policy.CreatedAt, policy.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetPolicy(tenantID, assetID string) (domain.MonitorPolicy, error) {
	var raw []byte
	err := p.pool.QueryRow(context.Background(), `
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

func (p *PostgresRepository) ListPolicies(tenantID string) ([]domain.MonitorPolicy, error) {
	rows, err := p.pool.Query(context.Background(), `
		SELECT config FROM monitor_policies WHERE tenant_id=$1 ORDER BY updated_at DESC`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.MonitorPolicy, 0)
	for rows.Next() {
		var raw []byte
		var policy domain.MonitorPolicy
		if err := rows.Scan(&raw); err != nil {
			return nil, normalizePostgresError(err)
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) CreateBaseline(baseline domain.BaselineVersion) error {
	snapshot, err := json.Marshal(baseline.Snapshot)
	if err != nil {
		return err
	}
	evidenceIDs, _ := json.Marshal(baseline.EvidenceIDs)
	_, err = p.pool.Exec(context.Background(), `
		INSERT INTO baseline_versions (
			id,tenant_id,asset_id,version,status,snapshot,evidence_ids,created_by,
			approved_by,created_at,approved_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,$11)`,
		baseline.ID, baseline.TenantID, baseline.AssetID, baseline.Version, baseline.Status,
		snapshot, evidenceIDs, baseline.CreatedBy, baseline.ApprovedBy, baseline.CreatedAt, baseline.ApprovedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateBaseline(baseline domain.BaselineVersion) error {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
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

func (p *PostgresRepository) GetBaseline(tenantID, id string) (domain.BaselineVersion, error) {
	return scanBaseline(p.pool.QueryRow(context.Background(), baselineSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) GetApprovedBaseline(tenantID, assetID string) (domain.BaselineVersion, error) {
	return scanBaseline(p.pool.QueryRow(context.Background(), baselineSelect+`
		WHERE tenant_id=$1 AND asset_id=$2 AND status='APPROVED' ORDER BY version DESC LIMIT 1`,
		tenantID, assetID))
}

func (p *PostgresRepository) ListBaselines(tenantID, assetID string) ([]domain.BaselineVersion, error) {
	query := baselineSelect + ` WHERE tenant_id=$1`
	args := []any{tenantID}
	if assetID != "" {
		query += ` AND asset_id=$2`
		args = append(args, assetID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := p.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.BaselineVersion, 0)
	for rows.Next() {
		item, scanErr := scanBaseline(rows)
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

func (p *PostgresRepository) CreateJob(job domain.DetectionJob) error {
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO detection_jobs (
			id,tenant_id,asset_id,mode,status,policy_version,baseline_version,error_code,
			error_message,retry_count,max_attempts,created_at,started_at,finished_at
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,0),NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14)`,
		job.ID, job.TenantID, job.AssetID, job.Mode, job.Status, job.PolicyVersion,
		job.BaselineVersion, job.ErrorCode, job.ErrorMessage, job.RetryCount, job.MaxAttempts, job.CreatedAt, job.StartedAt, job.FinishedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ClaimJob(tenantID, id string, startedAt time.Time) (bool, error) {
	leaseCutoff := startedAt.Add(-5 * time.Minute)
	tag, err := p.pool.Exec(context.Background(), `
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

func (p *PostgresRepository) UpdateJob(job domain.DetectionJob) error {
	tag, err := p.pool.Exec(context.Background(), `
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

func (p *PostgresRepository) GetJob(tenantID, id string) (domain.DetectionJob, error) {
	return scanJob(p.pool.QueryRow(context.Background(), jobSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListJobs(tenantID string) ([]domain.DetectionJob, error) {
	rows, err := p.pool.Query(context.Background(), jobSelect+` WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 1000`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.DetectionJob, 0)
	for rows.Next() {
		item, scanErr := scanJob(rows)
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

func (p *PostgresRepository) CreateEvidence(item domain.Evidence) error {
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO evidence (
			id,tenant_id,asset_id,job_id,kind,media_type,size_bytes,sha256,object_key,created_at,retain_until
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		item.ID, item.TenantID, item.AssetID, item.JobID, item.Kind, item.MediaType,
		item.Size, item.SHA256, item.ObjectKey, item.CreatedAt, item.RetainUntil,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) GetEvidence(tenantID, id string) (domain.Evidence, error) {
	return scanEvidence(p.pool.QueryRow(context.Background(), evidenceSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListEvidence(tenantID, assetID string) ([]domain.Evidence, error) {
	query := evidenceSelect + ` WHERE tenant_id=$1`
	args := []any{tenantID}
	if assetID != "" {
		query += ` AND asset_id=$2`
		args = append(args, assetID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := p.pool.Query(context.Background(), query, args...)
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

func (p *PostgresRepository) ListExpiredEvidence(tenantID string, before time.Time) ([]domain.Evidence, error) {
	rows, err := p.pool.Query(context.Background(), evidenceSelect+`
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

func (p *PostgresRepository) IsEvidenceReferenced(tenantID, id string) (bool, error) {
	var referenced bool
	err := p.pool.QueryRow(context.Background(), `
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

func (p *PostgresRepository) DeleteEvidence(tenantID, id string) error {
	tag, err := p.pool.Exec(context.Background(), `
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
		if queryErr := p.pool.QueryRow(context.Background(),
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

func (p *PostgresRepository) UpsertIncident(incident domain.Incident) (domain.Incident, bool, error) {
	signals, _ := json.Marshal(incident.Signals)
	evidenceIDs, _ := json.Marshal(incident.EvidenceIDs)
	row := p.pool.QueryRow(context.Background(), `
		INSERT INTO incidents (
			id,tenant_id,asset_id,baseline_id,baseline_version,fingerprint,severity,status,title,
			signals,evidence_ids,occurrence_count,owner_id,resolution,created_at,first_seen_at,last_seen_at,acknowledged_at,resolved_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid,NULLIF($14,''),$15,$16,$17,$18,$19)
		ON CONFLICT (tenant_id,asset_id,baseline_version,fingerprint)
			WHERE status NOT IN ('CLOSED','FALSE_POSITIVE')
		DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at,
			occurrence_count=incidents.occurrence_count+1,
			signals=EXCLUDED.signals,evidence_ids=incidents.evidence_ids || EXCLUDED.evidence_ids,
			severity=CASE
				WHEN EXCLUDED.severity='CRITICAL' THEN EXCLUDED.severity
				WHEN incidents.severity='CRITICAL' THEN incidents.severity
				ELSE EXCLUDED.severity
			END
		RETURNING (xmax = 0)`,
		incident.ID, incident.TenantID, incident.AssetID, incident.BaselineID, incident.BaselineVersion,
		incident.Fingerprint, incident.Severity, incident.Status, incident.Title, signals, evidenceIDs,
		incident.OccurrenceCount, incident.OwnerID, incident.Resolution, incident.CreatedAt,
		incident.FirstSeenAt, incident.LastSeenAt, incident.AcknowledgedAt, incident.ResolvedAt,
	)
	var created bool
	if err := row.Scan(&created); err != nil {
		return domain.Incident{}, false, normalizePostgresError(err)
	}
	incidents, err := p.ListIncidents(incident.TenantID)
	if err != nil {
		return domain.Incident{}, false, err
	}
	for _, current := range incidents {
		if current.AssetID == incident.AssetID && current.BaselineVersion == incident.BaselineVersion &&
			current.Fingerprint == incident.Fingerprint &&
			current.Status != domain.IncidentClosed && current.Status != domain.IncidentFalsePositive {
			return current, created, nil
		}
	}
	return incident, created, nil
}

func (p *PostgresRepository) UpdateIncident(incident domain.Incident) error {
	signals, _ := json.Marshal(incident.Signals)
	evidenceIDs, _ := json.Marshal(incident.EvidenceIDs)
	tag, err := p.pool.Exec(context.Background(), `
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

func (p *PostgresRepository) GetIncident(tenantID, id string) (domain.Incident, error) {
	return scanIncident(p.pool.QueryRow(context.Background(), incidentSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, id))
}

func (p *PostgresRepository) ListIncidents(tenantID string) ([]domain.Incident, error) {
	rows, err := p.pool.Query(context.Background(), incidentSelect+` WHERE tenant_id=$1 ORDER BY last_seen_at DESC`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.Incident, 0)
	for rows.Next() {
		item, scanErr := scanIncident(rows)
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

func (p *PostgresRepository) CreateAlertChannel(channel domain.AlertChannel) error {
	associatedData := channel.TenantID + ":" + channel.ID
	encryptedURL, err := p.secrets.Encrypt(channel.URL, associatedData+":url")
	if err != nil {
		return err
	}
	encryptedSecret, err := p.secrets.Encrypt(channel.Secret, associatedData+":secret")
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), `
		INSERT INTO alert_channels (
			id,tenant_id,name,channel_type,url,secret_ciphertext,enabled,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9)`,
		channel.ID, channel.TenantID, channel.Name, channel.Type, encryptedURL,
		encryptedSecret, channel.Enabled, channel.CreatedAt, channel.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ListAlertChannels(tenantID string) ([]domain.AlertChannel, error) {
	return p.listAlertChannels(tenantID, false)
}

func (p *PostgresRepository) ListAlertChannelsWithSecrets(tenantID string) ([]domain.AlertChannel, error) {
	return p.listAlertChannels(tenantID, true)
}

func (p *PostgresRepository) listAlertChannels(tenantID string, includeSecret bool) ([]domain.AlertChannel, error) {
	rows, err := p.pool.Query(context.Background(), `
		SELECT id,tenant_id,name,channel_type,url,COALESCE(secret_ciphertext,''),enabled,created_at,updated_at
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
			&item.ID, &item.TenantID, &item.Name, &item.Type, &encryptedURL, &encryptedSecret,
			&item.Enabled, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, normalizePostgresError(err)
		}
		associatedData := item.TenantID + ":" + item.ID
		item.URL, err = p.secrets.Decrypt(encryptedURL, associatedData+":url")
		if err != nil {
			// Values written before encryption are retained for a controlled
			// compatibility window and can be re-encrypted on next update.
			item.URL = encryptedURL
		}
		if includeSecret {
			item.Secret, err = p.secrets.Decrypt(encryptedSecret, associatedData+":secret")
			if err != nil {
				item.Secret = encryptedSecret
			}
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) CreateAlertDelivery(delivery domain.AlertDelivery) error {
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO alert_deliveries (
			id,tenant_id,incident_id,channel_id,dedup_key,status,attempts,last_error,next_attempt_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)`,
		delivery.ID, delivery.TenantID, delivery.IncidentID, delivery.ChannelID, delivery.DedupKey,
		delivery.Status, delivery.Attempts, delivery.LastError, delivery.NextAttemptAt, delivery.CreatedAt, delivery.UpdatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) UpdateAlertDelivery(delivery domain.AlertDelivery) error {
	tag, err := p.pool.Exec(context.Background(), `
		UPDATE alert_deliveries SET status=$3,attempts=$4,last_error=NULLIF($5,''),next_attempt_at=$6,updated_at=$7
		WHERE tenant_id=$1 AND id=$2`, delivery.TenantID, delivery.ID, delivery.Status,
		delivery.Attempts, delivery.LastError, delivery.NextAttemptAt, delivery.UpdatedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ListAlertDeliveries(tenantID, incidentID string) ([]domain.AlertDelivery, error) {
	query := `SELECT id,tenant_id,incident_id,channel_id,dedup_key,status,attempts,COALESCE(last_error,''),next_attempt_at,created_at,updated_at FROM alert_deliveries WHERE tenant_id=$1`
	args := []any{tenantID}
	if incidentID != "" {
		query += ` AND incident_id=$2`
		args = append(args, incidentID)
	}
	query += ` ORDER BY created_at DESC LIMIT 1000`
	rows, err := p.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, normalizePostgresError(err)
	}
	defer rows.Close()
	result := make([]domain.AlertDelivery, 0)
	for rows.Next() {
		var item domain.AlertDelivery
		if err := rows.Scan(&item.ID, &item.TenantID, &item.IncidentID, &item.ChannelID, &item.DedupKey,
			&item.Status, &item.Attempts, &item.LastError, &item.NextAttemptAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, normalizePostgresError(err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}
func (p *PostgresRepository) AppendAudit(event domain.AuditEvent) error {
	details, _ := json.Marshal(event.Details)
	_, err := p.pool.Exec(context.Background(), `
		INSERT INTO audit_events (
			id,tenant_id,actor_id,action,resource_type,resource_id,result,trace_id,details,created_at
		) VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10)`,
		event.ID, event.TenantID, event.ActorID, event.Action, event.ResourceType,
		event.ResourceID, event.Result, event.TraceID, details, event.CreatedAt,
	)
	return normalizePostgresError(err)
}

func (p *PostgresRepository) ListAudits(tenantID string) ([]domain.AuditEvent, error) {
	rows, err := p.pool.Query(context.Background(), `
		SELECT id,tenant_id,COALESCE(actor_id::text,''),action,resource_type,resource_id,
			result,trace_id,COALESCE(details,'{}'::jsonb),created_at
		FROM audit_events WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 1000`, tenantID)
	if err != nil {
		return nil, normalizePostgresError(err)
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
			return nil, normalizePostgresError(err)
		}
		if err := json.Unmarshal(details, &item.Details); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizePostgresError(err)
	}
	return result, nil
}

func (p *PostgresRepository) DeleteAuditsBefore(tenantID string, before time.Time) (int, error) {
	tag, err := p.pool.Exec(context.Background(),
		`DELETE FROM audit_events WHERE tenant_id=$1 AND created_at < $2`, tenantID, before)
	if err != nil {
		return 0, normalizePostgresError(err)
	}
	return int(tag.RowsAffected()), nil
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
