package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/config"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/maintenance"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/ratelimit"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// apiTracer attaches HTTP spans to the caller's trace. It resolves to the global
// no-op provider until telemetry.Setup installs a real one.
var apiTracer = otel.Tracer("webmonitoring/internal/api")

const (
	sessionCookie = "webmonitoring_session"
	defaultTenant = "00000000-0000-4000-8000-000000000001"
	// maxImportRows bounds a single batch import so one request cannot hold an
	// unbounded write loop on the control plane.
	maxImportRows = 500
)

type principalKey struct{}
type traceKey struct{}

type Server struct {
	repository        store.Repository
	auth              *auth.Service
	loginGuard        *auth.LoginGuard
	detection         *detection.Service
	evidence          evidence.Store
	guard             *security.URLGuard
	cipher            security.SecretBox
	broker            *events.Broker
	logger            *slog.Logger
	sessionSecure     bool
	allowedOrigins    map[string]struct{}
	rateLimiter       *ratelimit.Limiter
	metrics           *metrics.Registry
	readiness         func(context.Context) map[string]error
	evidenceRetention time.Duration
	auditRetention    time.Duration
	retention         *maintenance.Runner
	signedURLSigner   *security.SignedURLSigner
	signedURLTTL      time.Duration
	handler           http.Handler
}

func NewServer(
	repository store.Repository,
	authService *auth.Service,
	detectionService *detection.Service,
	evidenceStore evidence.Store,
	guard *security.URLGuard,
	broker *events.Broker,
	logger *slog.Logger,
	sessionSecure bool,
) *Server {
	server := &Server{
		repository:        repository,
		auth:              authService,
		detection:         detectionService,
		evidence:          evidenceStore,
		guard:             guard,
		broker:            broker,
		logger:            logger,
		sessionSecure:     sessionSecure,
		metrics:           metrics.New(),
		evidenceRetention: time.Duration(config.DefaultEvidenceRetentionDays) * 24 * time.Hour,
		auditRetention:    time.Duration(config.DefaultAuditRetentionDays) * 24 * time.Hour,
		signedURLTTL:      5 * time.Minute,
	}
	server.retention = maintenance.NewRunner(repository, evidenceStore, server.auditRetention, logger)
	server.handler = server.routes()
	return server
}

// SetAllowedOrigins turns on cross-origin responses for the listed origins.
// Without it the API is same-origin only, which is what the single-host Compose
// topology needs: the console and the API share an origin behind nginx.
func (s *Server) SetAllowedOrigins(origins map[string]struct{}) {
	s.allowedOrigins = origins
}

// SetRateLimiter installs the per-tenant request allowance. Without one the API
// is unlimited, which is what the tests and the embedded single-process mode
// get; a deployment sets it from REDIS_ADDR so every replica shares the count.
func (s *Server) SetRateLimiter(limiter *ratelimit.Limiter) {
	s.rateLimiter = limiter
}

func (s *Server) Metrics() *metrics.Registry {
	return s.metrics
}

func (s *Server) SetReadiness(check func(context.Context) map[string]error) {
	s.readiness = check
}

func (s *Server) SetRetentionPolicy(evidenceDays, auditDays int) {
	if evidenceDays > 0 {
		s.evidenceRetention = time.Duration(evidenceDays) * 24 * time.Hour
	}
	if auditDays > 0 {
		s.auditRetention = time.Duration(auditDays) * 24 * time.Hour
	}
	s.retention = maintenance.NewRunner(s.repository, s.evidence, s.auditRetention, s.logger)
}

// RetentionRunner exposes the shared retention runner so the process hosting
// the control plane can also drive the background pass.
func (s *Server) RetentionRunner() *maintenance.Runner {
	return s.retention
}

func (s *Server) SetLoginGuard(guard *auth.LoginGuard) {
	s.loginGuard = guard
}

// SetSecretCipher enables login-state detection. When set, updatePolicy can
// encrypt customer credentials; without it, enabling login detection fails
// closed with SECRET_UNAVAILABLE.
func (s *Server) SetSecretCipher(cipher security.SecretBox) {
	s.cipher = cipher
}

// SetSignedURLs enables control-plane-served signed evidence URLs (the local
// store path). S3 stores ignore the signer and issue presigned object URLs.
func (s *Server) SetSignedURLs(signer *security.SignedURLSigner, ttlSeconds int) {
	s.signedURLSigner = signer
	if ttlSeconds > 0 {
		s.signedURLTTL = time.Duration(ttlSeconds) * time.Second
	}
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) routes() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.health)
	root.HandleFunc("GET /readyz", s.ready)
	root.Handle("GET /metrics", s.metrics.Handler())
	root.HandleFunc("POST /api/v1/auth/session", s.login)
	root.HandleFunc("GET /api/v1/evidence/signed/{token}", s.downloadSignedEvidence)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/v1/auth/session", s.session)
	protected.HandleFunc("DELETE /api/v1/auth/session", s.logout)
	protected.HandleFunc("GET /api/v1/dashboard", s.dashboard)
	protected.HandleFunc("GET /api/v1/assets", s.listAssets)
	protected.HandleFunc("POST /api/v1/assets", s.requireRoles(s.createAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/assets/import", s.requireRoles(s.importAssets, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/assets/export", s.requireRoles(s.exportAssets, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/assets/{id}", s.getAsset)
	protected.HandleFunc("PATCH /api/v1/assets/{id}", s.requireRoles(s.updateAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/assets/{id}/verification", s.requireRoles(s.verifyAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/assets/{id}/suspend", s.requireRoles(s.suspendAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/assets/{id}/resume", s.requireRoles(s.resumeAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/policies", s.listPolicies)
	protected.HandleFunc("PUT /api/v1/policies/{assetId}", s.requireRoles(s.updatePolicy, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/baselines", s.listBaselines)
	// Readable by every authenticated role, like getIncident: the list endpoint
	// already serializes each baseline's full snapshot, so gating the detail read
	// would hide nothing that a list call does not already return.
	protected.HandleFunc("GET /api/v1/baselines/{id}", s.getBaseline)
	protected.HandleFunc("POST /api/v1/baselines/{id}/approve", s.requireRoles(s.approveBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/baselines/{id}/reject", s.requireRoles(s.rejectBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/baselines/{id}/retire", s.requireRoles(s.retireBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/detection-jobs", s.listJobs)
	protected.HandleFunc("POST /api/v1/detection-jobs", s.requireRoles(s.createJob, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/detection-jobs/{id}/cancel", s.requireRoles(s.cancelJob, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/incidents", s.listIncidents)
	protected.HandleFunc("GET /api/v1/incidents/{id}", s.getIncident)
	// Readable by every authenticated role, like getIncident: an auditor who can see
	// an incident but not how it was handled cannot audit it.
	protected.HandleFunc("GET /api/v1/incidents/{id}/timeline", s.listIncidentTimeline)
	protected.HandleFunc("POST /api/v1/incidents/{id}/comments", s.requireRoles(s.addIncidentComment, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/incidents/{id}/transition", s.requireRoles(s.transitionIncident, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/evidence", s.listEvidence)
	protected.HandleFunc("GET /api/v1/evidence/{id}/download", s.downloadEvidence)
	protected.HandleFunc("POST /api/v1/evidence/{id}/signed-url", s.requireRoles(s.signedEvidenceURL, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/alert-channels", s.listAlertChannels)
	protected.HandleFunc("POST /api/v1/alert-channels", s.requireRoles(s.createAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("PUT /api/v1/alert-channels/{id}", s.requireRoles(s.updateAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("DELETE /api/v1/alert-channels/{id}", s.requireRoles(s.deleteAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("POST /api/v1/alert-channels/{id}/test", s.requireRoles(s.testAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/alert-deliveries", s.listAlertDeliveries)
	protected.HandleFunc("GET /api/v1/audit-events", s.listAudits)
	protected.HandleFunc("POST /api/v1/maintenance/retention", s.requireRoles(s.runRetentionMaintenance, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/reports/summary", s.reportSummary)
	protected.HandleFunc("GET /api/v1/reports/{incidentId}", s.downloadReport)
	protected.HandleFunc("GET /api/v1/events/stream", s.streamEvents)
	protected.HandleFunc("GET /api/v1/sites", s.listSites)
	protected.HandleFunc("POST /api/v1/sites", s.requireRoles(s.createSite, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/sites/{id}", s.getSite)
	protected.HandleFunc("PATCH /api/v1/sites/{id}", s.requireRoles(s.updateSite, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/tenants", s.requireSuperAdmin(s.listTenants))
	protected.HandleFunc("POST /api/v1/tenants", s.requireSuperAdmin(s.createTenant))
	protected.HandleFunc("GET /api/v1/tenants/{id}", s.requireSuperAdmin(s.getTenant))
	protected.HandleFunc("PATCH /api/v1/tenants/{id}", s.requireSuperAdmin(s.updateTenant))
	protected.HandleFunc("DELETE /api/v1/tenants/{id}", s.requireSuperAdmin(s.deleteTenant))
	protected.HandleFunc("GET /api/v1/users", s.requireRoles(s.listUsers, domain.RoleAdmin))
	protected.HandleFunc("POST /api/v1/users", s.requireRoles(s.createUser, domain.RoleAdmin))
	protected.HandleFunc("PATCH /api/v1/users/{id}", s.requireRoles(s.updateUser, domain.RoleAdmin))
	protected.HandleFunc("POST /api/v1/users/{id}/password", s.requireRoles(s.resetUserPassword, domain.RoleAdmin))
	protected.HandleFunc("POST /api/v1/auth/password", s.changeOwnPassword)

	root.Handle("/api/v1/", s.authenticate(s.rateLimit(s.csrf(protected))))
	return s.trace(root)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(writer http.ResponseWriter, request *http.Request) {
	if s.readiness == nil {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	failures := s.readiness(ctx)
	if len(failures) == 0 {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	result := make(map[string]string, len(failures))
	for name, err := range failures {
		if err == nil {
			continue
		}
		result[name] = "unavailable"
		s.logger.Warn("readiness check failed", "dependency", name, "error", err)
	}
	writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
		"status": "not_ready", "dependencies": result,
	})
}

func (s *Server) login(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Tenant   string `json:"tenant"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	tenantID := defaultTenant
	if slug := strings.TrimSpace(input.Tenant); slug != "" {
		tenant, err := s.repository.GetTenantBySlug(slug)
		if err != nil {
			// Unified 401: do not reveal whether the tenant or the password was wrong.
			writeError(writer, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码错误", nil)
			return
		}
		tenantID = tenant.ID
	}
	username := strings.TrimSpace(input.Username)
	if err := s.loginGuard.BeforeLogin(request.Context(), tenantID, username, remoteIP(request)); err != nil {
		var locked *auth.ErrAccountLocked
		if errors.As(err, &locked) {
			writeError(writer, request, http.StatusUnauthorized, "ACCOUNT_LOCKED", "账户已锁定，请稍后再试",
				map[string]any{"retryAfterSeconds": int(locked.RetryAfter.Seconds())})
			return
		}
		var limited *auth.ErrRateLimited
		if errors.As(err, &limited) {
			writer.Header().Set("Retry-After", strconv.Itoa(int(limited.RetryAfter.Seconds())))
			writeError(writer, request, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁，请稍后再试", nil)
			return
		}
	}
	session, err := s.auth.Login(request.Context(), tenantID, username, input.Password)
	if err != nil {
		s.loginGuard.RecordFailure(request.Context(), tenantID, username)
		writeError(writer, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码错误", nil)
		return
	}
	s.loginGuard.RecordSuccess(request.Context(), tenantID, username)
	http.SetCookie(writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.sessionSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
	})
	s.appendAudit(request, session.User, "auth.login", "session", session.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusCreated, sessionResponse(session))
}

func (s *Server) session(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	writeJSON(writer, http.StatusOK, sessionResponse(session))
}

func (s *Server) logout(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	s.auth.Delete(request.Context(), session.ID)
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.sessionSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	s.appendAudit(request, session.User, "auth.logout", "session", session.ID, "SUCCESS", nil)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) dashboard(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	assets, _, err := s.repository.ListAssets(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list dashboard assets", err)
		return
	}
	incidents, _, err := s.repository.ListIncidents(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list dashboard incidents", err)
		return
	}
	jobs, _, err := s.repository.ListJobs(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list dashboard jobs", err)
		return
	}
	summary := map[string]int{
		"assets": len(assets), "verifiedAssets": 0, "openIncidents": 0,
		"criticalIncidents": 0, "failedJobs": 0,
	}
	for _, asset := range assets {
		if asset.VerificationStatus == domain.VerificationVerified {
			summary["verifiedAssets"]++
		}
	}
	for _, incident := range incidents {
		if incident.Status != domain.IncidentClosed && incident.Status != domain.IncidentFalsePositive {
			summary["openIncidents"]++
			if incident.Severity == domain.SeverityCritical {
				summary["criticalIncidents"]++
			}
		}
	}
	s.updateIncidentMetrics(incidents)
	for _, job := range jobs {
		if job.Status == domain.JobFailed || job.Status == domain.JobTimedOut {
			summary["failedJobs"]++
		}
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (s *Server) listAssets(writer http.ResponseWriter, request *http.Request) {
	siteID := request.URL.Query().Get("siteId")
	opts := listOptions(request)
	if siteID != "" {
		// A site-scoped listing is small; fetch unbounded so the post-filter
		// cannot cross the keyset page boundary.
		opts = store.ListOptions{}
	}
	items, nextCursor, err := s.repository.ListAssets(principal(request).User.TenantID, opts)
	if err != nil {
		s.repositoryFailure(writer, request, "list assets", err)
		return
	}
	if siteID != "" {
		filtered := make([]domain.Asset, 0, len(items))
		for _, asset := range items {
			if asset.SiteID == siteID {
				filtered = append(filtered, asset)
			}
		}
		items = filtered
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) getAsset(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetAsset(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

// assetInput is the accepted shape for asset creation. It is shared by the
// single-asset endpoint and the batch import endpoint so both go through
// exactly the same validation.
type assetInput struct {
	Name               string                    `json:"name"`
	URL                string                    `json:"url"`
	Type               domain.AssetType          `json:"type"`
	OwnerID            string                    `json:"ownerId"`
	SiteID             string                    `json:"siteId"`
	VerificationMethod domain.VerificationMethod `json:"verificationMethod"`
}

func (s *Server) createAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input assetInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	asset, code, message, err := s.buildAsset(request.Context(), session.User, input)
	if err != nil {
		status := http.StatusBadRequest
		if code == "ASSET_EXISTS" {
			status = http.StatusConflict
		}
		writeError(writer, request, status, code, message, nil)
		return
	}
	if err := s.repository.CreateAsset(asset); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "ASSET_EXISTS", "该 URL 已经登记", nil)
			return
		}
		s.repositoryFailure(writer, request, "create asset", err)
		return
	}
	if err := s.repository.UpsertPolicy(defaultPolicy(asset)); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "POLICY_CREATE_FAILED", "无法创建默认监测策略", nil)
		return
	}
	s.appendAudit(request, session.User, "asset.create", "asset", asset.ID, "SUCCESS", map[string]any{"url": asset.URL, "siteId": asset.SiteID})
	writeJSON(writer, http.StatusCreated, asset)
}

// buildAsset validates one asset definition and returns an unsaved domain.Asset
// scoped to owner's tenant. Every failure is returned as a machine-readable
// code plus a human-readable message; no state is written.
func (s *Server) buildAsset(
	ctx context.Context,
	owner domain.User,
	input assetInput,
) (domain.Asset, string, string, error) {
	parsed, _, err := s.guard.Validate(ctx, input.URL)
	if err != nil {
		return domain.Asset{}, "UNSAFE_TARGET", "目标 URL 不符合安全策略", err
	}
	if strings.TrimSpace(input.Name) == "" || !validAssetType(input.Type) {
		return domain.Asset{}, "INVALID_ASSET", "资产名称或类型无效", errors.New("invalid asset name or type")
	}
	method := input.VerificationMethod
	if method == "" {
		method = domain.VerificationFile
	}
	if method != domain.VerificationFile && method != domain.VerificationDNS && method != domain.VerificationMeta {
		return domain.Asset{}, "INVALID_ASSET", "资产验证方式无效", errors.New("invalid verification method")
	}
	now := time.Now().UTC()
	asset := domain.Asset{
		ID:                 identity.New(),
		TenantID:           owner.TenantID,
		SiteID:             input.SiteID,
		Name:               strings.TrimSpace(input.Name),
		URL:                canonicalURL(parsed),
		Type:               input.Type,
		OwnerID:            input.OwnerID,
		Enabled:            false,
		VerificationStatus: domain.VerificationPending,
		VerificationMethod: method,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if asset.OwnerID == "" {
		asset.OwnerID = owner.ID
	} else if _, err := s.repository.GetUser(owner.TenantID, asset.OwnerID); err != nil {
		return domain.Asset{}, "INVALID_OWNER", "负责人不属于当前租户", err
	}
	if asset.SiteID != "" {
		if _, err := s.repository.GetSite(owner.TenantID, asset.SiteID); err != nil {
			return domain.Asset{}, "INVALID_SITE", "站点不属于当前租户", err
		}
	}
	return asset, "", "", nil
}

// importAssets creates many assets from one request. Rows are independent: a
// rejected row does not roll back the rows that succeeded, and the response
// reports each row's outcome so the caller can retry only the failures. Every
// row goes through buildAsset, so SSRF, ownership and tenant checks are
// identical to the single-asset endpoint.
func (s *Server) importAssets(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Assets  []assetInput `json:"assets"`
		DryRun  bool         `json:"dryRun"`
		SiteID  string       `json:"siteId"`
		OwnerID string       `json:"ownerId"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if len(input.Assets) == 0 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_IMPORT", "导入内容为空", nil)
		return
	}
	if len(input.Assets) > maxImportRows {
		writeError(writer, request, http.StatusBadRequest, "IMPORT_TOO_LARGE", "单次导入行数超过上限", map[string]any{"limit": maxImportRows})
		return
	}
	type rowResult struct {
		Index   int    `json:"index"`
		AssetID string `json:"assetId,omitempty"`
		Name    string `json:"name,omitempty"`
		URL     string `json:"url,omitempty"`
		Status  string `json:"status"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}
	results := make([]rowResult, 0, len(input.Assets))
	created := 0
	for index, row := range input.Assets {
		// Request-level defaults apply only when the row does not set them.
		if row.SiteID == "" {
			row.SiteID = input.SiteID
		}
		if row.OwnerID == "" {
			row.OwnerID = input.OwnerID
		}
		asset, code, message, err := s.buildAsset(request.Context(), session.User, row)
		if err != nil {
			results = append(results, rowResult{Index: index, Name: row.Name, URL: row.URL, Status: "REJECTED", Code: code, Message: message})
			continue
		}
		if input.DryRun {
			results = append(results, rowResult{Index: index, Name: asset.Name, URL: asset.URL, Status: "VALIDATED"})
			continue
		}
		if err := s.repository.CreateAsset(asset); err != nil {
			status, code, message := "REJECTED", "IMPORT_FAILED", "无法写入资产"
			if errors.Is(err, store.ErrConflict) {
				code, message = "ASSET_EXISTS", "该 URL 已经登记"
			}
			results = append(results, rowResult{Index: index, Name: asset.Name, URL: asset.URL, Status: status, Code: code, Message: message})
			continue
		}
		if err := s.repository.UpsertPolicy(defaultPolicy(asset)); err != nil {
			results = append(results, rowResult{Index: index, Name: asset.Name, URL: asset.URL, Status: "REJECTED", Code: "POLICY_CREATE_FAILED", Message: "无法创建默认监测策略"})
			continue
		}
		created++
		results = append(results, rowResult{Index: index, AssetID: asset.ID, Name: asset.Name, URL: asset.URL, Status: "CREATED"})
	}
	s.appendAudit(request, session.User, "asset.import", "asset", "", "SUCCESS", map[string]any{
		"rows": len(input.Assets), "created": created, "dryRun": input.DryRun,
	})
	writeJSON(writer, http.StatusOK, map[string]any{
		"dryRun": input.DryRun, "rows": len(input.Assets), "created": created, "results": results,
	})
}

// exportAssets returns every asset in the caller's tenant as CSV or JSON. The
// export is tenant-scoped from the session, never from a query parameter.
func (s *Server) exportAssets(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	format := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_FORMAT", "导出格式必须是 json 或 csv", nil)
		return
	}
	// Export is an unbounded, explicit operation, so it bypasses the keyset page.
	items, _, err := s.repository.ListAssets(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "export assets", err)
		return
	}
	s.appendAudit(request, session.User, "asset.export", "asset", "", "SUCCESS", map[string]any{
		"count": len(items), "format": format,
	})
	if format == "csv" {
		writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
		writer.Header().Set("Content-Disposition", `attachment; filename="assets.csv"`)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "no-store")
		csvWriter := csv.NewWriter(writer)
		_ = csvWriter.Write([]string{"id", "name", "url", "type", "siteId", "ownerId", "enabled", "verificationStatus", "verificationMethod", "createdAt"})
		for _, asset := range items {
			_ = csvWriter.Write([]string{
				asset.ID, asset.Name, asset.URL, string(asset.Type), asset.SiteID, asset.OwnerID,
				strconv.FormatBool(asset.Enabled), string(asset.VerificationStatus), string(asset.VerificationMethod),
				asset.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		csvWriter.Flush()
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteTenant(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Confirm string `json:"confirm"`
		Cascade bool   `json:"cascade"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	tenant, err := s.repository.GetTenant(request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	// Fail closed on the irreversible cases before touching any data. Deleting
	// the caller's own tenant would also destroy the audit trail that records
	// the deletion, so it is refused outright.
	if tenant.ID == session.User.TenantID {
		writeError(writer, request, http.StatusConflict, "TENANT_SELF_DELETE", "不能删除当前登录的租户", nil)
		return
	}
	if tenant.ID == domain.DefaultTenantID {
		writeError(writer, request, http.StatusConflict, "TENANT_PROTECTED", "默认租户不可删除", nil)
		return
	}
	// The confirmation must echo the slug; the path already pins the ID.
	if input.Confirm != tenant.Slug {
		writeError(writer, request, http.StatusBadRequest, "CONFIRM_MISMATCH", "确认信息与租户标识不一致", map[string]any{"expected": "slug"})
		return
	}
	counts, err := s.repository.CountTenantResources(tenant.ID)
	if err != nil {
		s.repositoryFailure(writer, request, "count tenant resources", err)
		return
	}
	total := 0
	for _, count := range counts {
		total += count
	}
	if total > 0 && !input.Cascade {
		details := make(map[string]any, len(counts))
		for kind, count := range counts {
			details[kind] = count
		}
		writeError(writer, request, http.StatusConflict, "TENANT_NOT_EMPTY", "租户仍有关联数据，需显式级联删除", details)
		return
	}
	// Audit before the delete so the record survives in the caller's tenant.
	s.appendAudit(request, session.User, "tenant.delete", "tenant", tenant.ID, "SUCCESS", map[string]any{
		"slug": tenant.Slug, "cascade": input.Cascade, "resources": counts,
	})
	deleteEvidenceObjects := s.evidence != nil
	var skippedObjects int
	if deleteEvidenceObjects {
		items, _, listErr := s.repository.ListEvidence(tenant.ID, "", store.ListOptions{})
		if listErr != nil {
			s.repositoryFailure(writer, request, "list tenant evidence", listErr)
			return
		}
		for _, item := range items {
			if deleteErr := s.evidence.Delete(item.ObjectKey); deleteErr != nil {
				skippedObjects++
				s.logger.Warn("delete tenant evidence object failed", "evidence", item.ID, "error", deleteErr)
			}
		}
	}
	if err := s.repository.DeleteTenant(tenant.ID); err != nil {
		s.repositoryFailure(writer, request, "delete tenant", err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"tenantId": tenant.ID, "deletedResources": counts, "orphanedEvidenceObjects": skippedObjects,
	})
}

func (s *Server) updateAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	asset, err := s.repository.GetAsset(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input struct {
		Name    *string `json:"name"`
		OwnerID *string `json:"ownerId"`
		SiteID  *string `json:"siteId"`
		Enabled *bool   `json:"enabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) != "" {
		asset.Name = strings.TrimSpace(*input.Name)
	}
	if input.OwnerID != nil && *input.OwnerID != "" {
		if _, err := s.repository.GetUser(session.User.TenantID, *input.OwnerID); err != nil {
			writeError(writer, request, http.StatusBadRequest, "INVALID_OWNER", "负责人不属于当前租户", nil)
			return
		}
		asset.OwnerID = *input.OwnerID
	}
	if input.SiteID != nil {
		if *input.SiteID == "" {
			asset.SiteID = ""
		} else {
			if _, err := s.repository.GetSite(session.User.TenantID, *input.SiteID); err != nil {
				writeError(writer, request, http.StatusBadRequest, "INVALID_SITE", "站点不属于当前租户", nil)
				return
			}
			asset.SiteID = *input.SiteID
		}
	}
	if input.Enabled != nil {
		if *input.Enabled && asset.VerificationStatus != domain.VerificationVerified {
			writeError(writer, request, http.StatusConflict, "ASSET_NOT_VERIFIED", "未验证资产不能启用", nil)
			return
		}
		asset.Enabled = *input.Enabled
	}
	asset.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateAsset(asset); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "无法更新资产", nil)
		return
	}
	s.appendAudit(request, session.User, "asset.update", "asset", asset.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, asset)
}

func (s *Server) verifyAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	asset, err := s.repository.GetAsset(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if strings.ToUpper(input.Action) == "ISSUE" {
		now := time.Now().UTC()
		value := "webmonitoring-verification=" + identity.New()
		expires := now.Add(30 * time.Minute)
		asset.VerificationToken = ""
		asset.VerificationTokenHash = fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
		asset.VerificationIssuedAt = &now
		asset.VerificationExpiresAt = &expires
		asset.VerificationStatus = domain.VerificationPending
		asset.UpdatedAt = now
		if err := s.repository.UpdateAsset(asset); err != nil {
			writeError(writer, request, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "无法保存验证状态", nil)
			return
		}
		s.appendAudit(request, session.User, "asset.verification.issue", "asset", asset.ID, "SUCCESS", map[string]any{"method": assetMethod(asset)})
		writeJSON(writer, http.StatusOK, verificationInstructions(asset, value))
		return
	}
	if strings.ToUpper(input.Action) != "VERIFY" || (asset.VerificationToken == "" && asset.VerificationTokenHash == "") {
		writeError(writer, request, http.StatusBadRequest, "INVALID_ACTION", "请先签发验证令牌", nil)
		return
	}
	if asset.VerificationExpiresAt != nil && time.Now().UTC().After(*asset.VerificationExpiresAt) {
		asset.VerificationStatus = domain.VerificationRejected
		asset.Enabled = false
		asset.UpdatedAt = time.Now().UTC()
		if updateErr := s.repository.UpdateAsset(asset); updateErr != nil {
			s.logger.Error("persist expired verification state", "asset_id", asset.ID, "error", updateErr)
		}
		s.appendAudit(request, session.User, "asset.verification.verify", "asset", asset.ID, "FAILED", map[string]any{"reason": "expired"})
		writeError(writer, request, http.StatusUnprocessableEntity, "VERIFICATION_EXPIRED", "验证令牌已过期，请重新签发", nil)
		return
	}
	if err := s.checkVerification(request.Context(), asset); err != nil {
		asset.VerificationStatus = domain.VerificationRejected
		asset.Enabled = false
		asset.UpdatedAt = time.Now().UTC()
		if updateErr := s.repository.UpdateAsset(asset); updateErr != nil {
			s.logger.Error("persist failed verification state", "asset_id", asset.ID, "error", updateErr)
		}
		s.appendAudit(request, session.User, "asset.verification.verify", "asset", asset.ID, "FAILED", nil)
		writeError(writer, request, http.StatusUnprocessableEntity, "VERIFICATION_FAILED", "未在目标站点找到有效令牌", nil)
		return
	}
	asset.VerificationStatus = domain.VerificationVerified
	asset.Enabled = true
	asset.VerificationToken = ""
	asset.VerificationTokenHash = ""
	asset.VerificationExpiresAt = nil
	asset.UpdatedAt = time.Now().UTC()
	_ = s.repository.UpdateAsset(asset)
	s.appendAudit(request, session.User, "asset.verification.verify", "asset", asset.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, asset)
}

// suspendAsset stops monitoring a verified asset by moving it to SUSPENDED and
// disabling it. Only a VERIFIED asset can be suspended; any other state is a
// conflict. The scheduler and Service.Queue both skip non-verified/disabled
// assets, so suspension immediately halts detection.
func (s *Server) suspendAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	asset, err := s.repository.GetAsset(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	if asset.VerificationStatus != domain.VerificationVerified {
		writeError(writer, request, http.StatusConflict, "INVALID_ASSET_STATE", "仅已验证资产可暂停", nil)
		return
	}
	asset.VerificationStatus = domain.VerificationSuspended
	asset.Enabled = false
	asset.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateAsset(asset); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "无法暂停资产", nil)
		return
	}
	s.appendAudit(request, session.User, "asset.suspend", "asset", asset.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, asset)
}

// resumeAsset returns a suspended asset to VERIFIED without re-enabling it.
// Re-enabling goes through the existing PATCH path, which already requires the
// asset to be VERIFIED.
func (s *Server) resumeAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	asset, err := s.repository.GetAsset(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	if asset.VerificationStatus != domain.VerificationSuspended {
		writeError(writer, request, http.StatusConflict, "INVALID_ASSET_STATE", "仅已暂停资产可恢复", nil)
		return
	}
	asset.VerificationStatus = domain.VerificationVerified
	asset.Enabled = false
	asset.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateAsset(asset); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "无法恢复资产", nil)
		return
	}
	s.appendAudit(request, session.User, "asset.resume", "asset", asset.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, asset)
}

func (s *Server) listSites(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListSites(principal(request).User.TenantID, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list sites", err)
		return
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) getSite(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetSite(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (s *Server) createSite(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Name    string `json:"name"`
		Domain  string `json:"domain"`
		OwnerID string `json:"ownerId"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Domain) == "" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_SITE", "站点名称或域名无效", nil)
		return
	}
	ownerID := input.OwnerID
	if ownerID == "" {
		ownerID = session.User.ID
	} else if _, err := s.repository.GetUser(session.User.TenantID, ownerID); err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_OWNER", "负责人不属于当前租户", nil)
		return
	}
	now := time.Now().UTC()
	site := domain.Site{
		ID: identity.New(), TenantID: session.User.TenantID,
		Name: strings.TrimSpace(input.Name), Domain: strings.TrimSpace(input.Domain),
		OwnerID: ownerID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.CreateSite(site); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "SITE_EXISTS", "站点名称已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "create site", err)
		return
	}
	s.appendAudit(request, session.User, "site.create", "site", site.ID, "SUCCESS", map[string]any{"domain": site.Domain})
	writeJSON(writer, http.StatusCreated, site)
}

func (s *Server) updateSite(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	site, err := s.repository.GetSite(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input struct {
		Name    *string `json:"name"`
		Domain  *string `json:"domain"`
		OwnerID *string `json:"ownerId"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) != "" {
		site.Name = strings.TrimSpace(*input.Name)
	}
	if input.Domain != nil && strings.TrimSpace(*input.Domain) != "" {
		site.Domain = strings.TrimSpace(*input.Domain)
	}
	if input.OwnerID != nil && *input.OwnerID != "" {
		if _, err := s.repository.GetUser(session.User.TenantID, *input.OwnerID); err != nil {
			writeError(writer, request, http.StatusBadRequest, "INVALID_OWNER", "负责人不属于当前租户", nil)
			return
		}
		site.OwnerID = *input.OwnerID
	}
	site.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateSite(site); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "SITE_EXISTS", "站点名称已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "update site", err)
		return
	}
	s.appendAudit(request, session.User, "site.update", "site", site.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, site)
}

func (s *Server) listTenants(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListTenants(listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list tenants", err)
		return
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) getTenant(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetTenant(request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (s *Server) createTenant(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Name          string `json:"name"`
		Slug          string `json:"slug"`
		AdminUsername string `json:"adminUsername"`
		AdminPassword string `json:"adminPassword"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	slug, ok := normalizeSlug(input.Slug)
	if strings.TrimSpace(input.Name) == "" || !ok {
		writeError(writer, request, http.StatusBadRequest, "INVALID_TENANT", "租户名称或标识无效", nil)
		return
	}
	if strings.TrimSpace(input.AdminUsername) == "" || len(input.AdminPassword) < 8 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_ADMIN", "管理员用户名或密码不满足要求", nil)
		return
	}
	now := time.Now().UTC()
	tenant := domain.Tenant{
		ID: identity.New(), Slug: slug, Name: strings.TrimSpace(input.Name), CreatedAt: now,
	}
	if err := s.repository.CreateTenant(tenant); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "TENANT_EXISTS", "租户标识已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "create tenant", err)
		return
	}
	hash, err := security.HashPassword(input.AdminPassword)
	if err != nil {
		s.logger.Error("hash tenant admin password", "tenant", tenant.ID, "error", err)
		writeError(writer, request, http.StatusInternalServerError, "TENANT_ADMIN_FAILED", "无法初始化租户管理员", nil)
		return
	}
	admin := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: strings.TrimSpace(input.AdminUsername),
		PasswordHash: hash, Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := s.repository.CreateUser(admin); err != nil {
		s.logger.Error("seed tenant admin", "tenant", tenant.ID, "error", err)
		writeError(writer, request, http.StatusInternalServerError, "TENANT_ADMIN_FAILED", "无法初始化租户管理员", nil)
		return
	}
	s.appendAudit(request, session.User, "tenant.create", "tenant", tenant.ID, "SUCCESS", map[string]any{"slug": tenant.Slug})
	writeJSON(writer, http.StatusCreated, tenant)
}

func (s *Server) updateTenant(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	tenant, err := s.repository.GetTenant(request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input struct {
		Name     *string `json:"name"`
		Slug     *string `json:"slug"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) != "" {
		tenant.Name = strings.TrimSpace(*input.Name)
	}
	if input.Slug != nil && strings.TrimSpace(*input.Slug) != "" {
		slug, ok := normalizeSlug(*input.Slug)
		if !ok {
			writeError(writer, request, http.StatusBadRequest, "INVALID_TENANT", "租户标识无效", nil)
			return
		}
		tenant.Slug = slug
	}
	if input.Disabled != nil {
		// Fail closed: the platform's own tenant and the caller's tenant must
		// stay administrable, otherwise a single request can lock everyone out.
		if *input.Disabled && (tenant.ID == domain.DefaultTenantID || tenant.ID == session.User.TenantID) {
			writeError(writer, request, http.StatusConflict, "TENANT_PROTECTED", "默认租户或当前登录租户不可停用", nil)
			return
		}
		tenant.Disabled = *input.Disabled
	}
	if err := s.repository.UpdateTenant(tenant); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "TENANT_EXISTS", "租户标识已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "update tenant", err)
		return
	}
	s.appendAudit(request, session.User, "tenant.update", "tenant", tenant.ID, "SUCCESS", map[string]any{"disabled": tenant.Disabled})
	writeJSON(writer, http.StatusOK, tenant)
}

func (s *Server) listUsers(writer http.ResponseWriter, request *http.Request) {
	items, err := s.repository.ListUsers(principal(request).User.TenantID)
	if err != nil {
		s.repositoryFailure(writer, request, "list users", err)
		return
	}
	writeList(writer, items, "")
}

func (s *Server) createUser(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Username string      `json:"username"`
		Password string      `json:"password"`
		Role     domain.Role `json:"role"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	username := strings.TrimSpace(input.Username)
	if username == "" || len(input.Password) < 12 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_USER", "用户名或密码不满足要求", nil)
		return
	}
	// Tenant admins may only provision operational or auditor accounts; admin and
	// platform super-admin accounts are created elsewhere (tenant bootstrap).
	if input.Role != domain.RoleOperator && input.Role != domain.RoleAuditor {
		writeError(writer, request, http.StatusBadRequest, "INVALID_ROLE", "仅可创建 OPERATOR 或 AUDITOR 用户", nil)
		return
	}
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		s.logger.Error("hash user password", "error", err)
		writeError(writer, request, http.StatusBadRequest, "INVALID_PASSWORD", "密码不满足要求", nil)
		return
	}
	now := time.Now().UTC()
	user := domain.User{
		ID: identity.New(), TenantID: session.User.TenantID, Username: username,
		PasswordHash: hash, Role: input.Role, CreatedAt: now,
	}
	if err := s.repository.CreateUser(user); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "USER_EXISTS", "用户名已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "create user", err)
		return
	}
	s.appendAudit(request, session.User, "user.create", "user", user.ID, "SUCCESS", map[string]any{"username": user.Username, "role": string(user.Role)})
	writeJSON(writer, http.StatusCreated, user)
}

func (s *Server) updateUser(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	user, err := s.repository.GetUser(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	// The bootstrap SUPER_ADMIN is a platform account, not tenant-managed.
	if user.Role == domain.RoleSuperAdmin {
		writeError(writer, request, http.StatusForbidden, "SUPER_ADMIN_PROTECTED", "不能修改平台超级管理员", nil)
		return
	}
	wasEnabledAdmin := user.Role == domain.RoleAdmin && !user.Disabled
	var input struct {
		Username *string      `json:"username"`
		Role     *domain.Role `json:"role"`
		Disabled *bool        `json:"disabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.Username != nil && strings.TrimSpace(*input.Username) != "" {
		user.Username = strings.TrimSpace(*input.Username)
	}
	if input.Role != nil {
		if !validUserRole(*input.Role) {
			writeError(writer, request, http.StatusBadRequest, "INVALID_ROLE", "角色无效", nil)
			return
		}
		user.Role = *input.Role
	}
	if input.Disabled != nil {
		user.Disabled = *input.Disabled
	}
	// Fail closed: an admin cannot disable or demote themselves.
	if user.ID == session.User.ID && (user.Disabled || user.Role != session.User.Role) {
		writeError(writer, request, http.StatusConflict, "SELF_DEMOTION", "不能禁用或降级自己", nil)
		return
	}
	// A tenant must always retain at least one enabled admin.
	if wasEnabledAdmin && (user.Disabled || user.Role != domain.RoleAdmin) {
		if remaining, countErr := s.countOtherEnabledAdmins(session.User.TenantID, user.ID); countErr == nil && remaining == 0 {
			writeError(writer, request, http.StatusConflict, "LAST_ADMIN", "租户内至少需保留一名启用的管理员", nil)
			return
		}
	}
	if err := s.repository.UpdateUser(user); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(writer, request, http.StatusConflict, "USER_EXISTS", "用户名已存在", nil)
			return
		}
		s.repositoryFailure(writer, request, "update user", err)
		return
	}
	s.appendAudit(request, session.User, "user.update", "user", user.ID, "SUCCESS", map[string]any{"role": string(user.Role), "disabled": user.Disabled})
	writeJSON(writer, http.StatusOK, user)
}

func (s *Server) resetUserPassword(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	user, err := s.repository.GetUser(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	if user.Role == domain.RoleSuperAdmin {
		writeError(writer, request, http.StatusForbidden, "SUPER_ADMIN_PROTECTED", "不能修改平台超级管理员", nil)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if len(input.Password) < 12 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_PASSWORD", "密码至少 12 位", nil)
		return
	}
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_PASSWORD", "密码不满足要求", nil)
		return
	}
	user.PasswordHash = hash
	if err := s.repository.UpdateUser(user); err != nil {
		s.repositoryFailure(writer, request, "reset user password", err)
		return
	}
	s.appendAudit(request, session.User, "user.password.reset", "user", user.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusNoContent, nil)
}

func (s *Server) changeOwnPassword(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if !security.VerifyPassword(session.User.PasswordHash, input.CurrentPassword) {
		writeError(writer, request, http.StatusUnauthorized, "INVALID_PASSWORD", "当前密码不正确", nil)
		return
	}
	if len(input.NewPassword) < 12 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_PASSWORD", "密码至少 12 位", nil)
		return
	}
	hash, err := security.HashPassword(input.NewPassword)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_PASSWORD", "密码不满足要求", nil)
		return
	}
	user := session.User
	user.PasswordHash = hash
	if err := s.repository.UpdateUser(user); err != nil {
		s.repositoryFailure(writer, request, "change password", err)
		return
	}
	s.appendAudit(request, session.User, "auth.password.change", "user", user.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusNoContent, nil)
}

func (s *Server) countOtherEnabledAdmins(tenantID, excludeID string) (int, error) {
	users, err := s.repository.ListUsers(tenantID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, user := range users {
		if user.ID != excludeID && user.Role == domain.RoleAdmin && !user.Disabled {
			count++
		}
	}
	return count, nil
}

func validUserRole(role domain.Role) bool {
	return role == domain.RoleAdmin || role == domain.RoleOperator || role == domain.RoleAuditor
}

func (s *Server) listPolicies(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListPolicies(principal(request).User.TenantID, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list policies", err)
		return
	}
	redacted := make([]domain.MonitorPolicy, len(items))
	for i := range items {
		redacted[i] = redactPolicy(items[i])
	}
	writeList(writer, redacted, nextCursor)
}

func (s *Server) updatePolicy(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	assetID := request.PathValue("assetId")
	if _, err := s.repository.GetAsset(session.User.TenantID, assetID); err != nil {
		s.notFound(writer, request)
		return
	}
	current, err := s.repository.GetPolicy(session.User.TenantID, assetID)
	if err != nil {
		writeError(writer, request, http.StatusNotFound, "POLICY_NOT_FOUND", "未找到监测策略", nil)
		return
	}
	var input struct {
		HTTPIntervalSeconds    int             `json:"httpIntervalSeconds"`
		BrowserIntervalSeconds int             `json:"browserIntervalSeconds"`
		TimeoutSeconds         int             `json:"timeoutSeconds"`
		MaxResponseBytes       int64           `json:"maxResponseBytes"`
		IgnoreSelectors        []string        `json:"ignoreSelectors"`
		TrustedDomains         []string        `json:"trustedDomains"`
		MaintenanceWindowStart *time.Time      `json:"maintenanceWindowStart"`
		MaintenanceWindowEnd   *time.Time      `json:"maintenanceWindowEnd"`
		Auth                   json.RawMessage `json:"auth"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.HTTPIntervalSeconds < 60 || input.BrowserIntervalSeconds < 300 ||
		input.TimeoutSeconds < 1 || input.TimeoutSeconds > 90 ||
		input.MaxResponseBytes < 1024 || input.MaxResponseBytes > 10*1024*1024 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_POLICY", "监测策略超出允许范围", nil)
		return
	}
	for _, selector := range input.IgnoreSelectors {
		if _, err := detection.AnalyzeHTML([]byte("<html></html>"), "https://example.com", []string{selector}); err != nil {
			writeError(writer, request, http.StatusBadRequest, "INVALID_SELECTOR", err.Error(), nil)
			return
		}
	}
	now := time.Now().UTC()
	current.Version++
	current.HTTPIntervalSeconds = input.HTTPIntervalSeconds
	current.BrowserIntervalSeconds = input.BrowserIntervalSeconds
	current.TimeoutSeconds = input.TimeoutSeconds
	current.MaxResponseBytes = input.MaxResponseBytes
	current.IgnoreSelectors = append([]string(nil), input.IgnoreSelectors...)
	current.TrustedDomains = append([]string(nil), input.TrustedDomains...)
	current.MaintenanceWindowStart = input.MaintenanceWindowStart
	current.MaintenanceWindowEnd = input.MaintenanceWindowEnd
	// A missing "auth" key preserves existing login state; an explicit null
	// disables it; an object enables or reconfigures it.
	if len(input.Auth) > 0 {
		if bytes.Equal(bytes.TrimSpace(input.Auth), []byte("null")) {
			current.Auth = nil
		} else if authConfig, authErr := s.buildAuthConfig(request.Context(), session.User.TenantID, assetID, input.Auth, current.Auth); authErr != nil {
			writeError(writer, request, http.StatusBadRequest, "INVALID_AUTH", authErr.Error(), nil)
			return
		} else {
			current.Auth = authConfig
		}
		action := "detection.login_state.disabled"
		if current.Auth != nil {
			action = "detection.login_state.enabled"
		}
		s.appendAudit(request, session.User, action, "policy", current.ID, "SUCCESS", map[string]any{"version": current.Version})
	}
	current.UpdatedAt = now
	if err := s.repository.UpsertPolicy(current); err != nil {
		s.repositoryFailure(writer, request, "update policy", err)
		return
	}
	s.appendAudit(request, session.User, "policy.update", "policy", current.ID, "SUCCESS", map[string]any{"version": current.Version})
	writeJSON(writer, http.StatusOK, redactPolicy(current))
}

// authInput is the login-state portion of an updatePolicy request. Username and
// password arrive as plaintext only here, in the request body, and are encrypted
// before they touch the repository.
type authInput struct {
	LoginURL       string            `json:"loginUrl"`
	UsernameField  string            `json:"usernameField"`
	PasswordField  string            `json:"passwordField"`
	ExtraFields    map[string]string `json:"extraFields"`
	SubmitSelector string            `json:"submitSelector"`
	Username       string            `json:"username"`
	Password       string            `json:"password"`
}

// buildAuthConfig parses and validates a login-state config. Credentials are
// never stored in plaintext: they are AES-GCM encrypted with associated data
// bound to the tenant and asset. Re-sending the config without a password
// preserves any previously stored credentials.
func (s *Server) buildAuthConfig(ctx context.Context, tenantID, assetID string, raw json.RawMessage, existing *domain.AuthConfig) (*domain.AuthConfig, error) {
	var input authInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, errors.New("登录态检测配置格式错误")
	}
	if input.LoginURL == "" || input.UsernameField == "" || input.PasswordField == "" {
		return nil, errors.New("loginUrl、usernameField、passwordField 为必填项")
	}
	if _, _, err := s.guard.Validate(ctx, input.LoginURL); err != nil {
		return nil, fmt.Errorf("登录地址不在允许范围内: %v", err)
	}
	auth := &domain.AuthConfig{
		LoginURL:       input.LoginURL,
		UsernameField:  input.UsernameField,
		PasswordField:  input.PasswordField,
		SubmitSelector: input.SubmitSelector,
		ExtraFields:    input.ExtraFields,
	}
	if existing != nil {
		auth.EncryptedUsername = existing.EncryptedUsername
		auth.EncryptedPassword = existing.EncryptedPassword
	}
	if input.Username != "" || input.Password != "" {
		if input.Username == "" || input.Password == "" {
			return nil, errors.New("用户名和密码必须同时提供")
		}
		if s.cipher == nil {
			return nil, errors.New("登录态检测需要配置 ALERT_SECRET_KEY")
		}
		associatedData := security.AuthAssociatedData(tenantID, assetID)
		encryptedUsername, err := s.cipher.Encrypt(input.Username, associatedData)
		if err != nil {
			return nil, err
		}
		encryptedPassword, err := s.cipher.Encrypt(input.Password, associatedData)
		if err != nil {
			return nil, err
		}
		auth.EncryptedUsername = encryptedUsername
		auth.EncryptedPassword = encryptedPassword
	}
	auth.CredentialsSet = auth.EncryptedUsername != "" || auth.EncryptedPassword != ""
	if !auth.CredentialsSet {
		return nil, errors.New("启用登录态检测必须提供用户名和密码")
	}
	return auth, nil
}

// redactPolicy strips encrypted credentials before a policy is returned so that
// ciphertext never leaves the control plane. CredentialsSet is derived from the
// stored ciphertext so clients can still tell whether login is configured.
func redactPolicy(policy domain.MonitorPolicy) domain.MonitorPolicy {
	if policy.Auth == nil {
		return policy
	}
	auth := *policy.Auth
	auth.CredentialsSet = auth.EncryptedUsername != "" || auth.EncryptedPassword != ""
	auth.EncryptedUsername = ""
	auth.EncryptedPassword = ""
	policy.Auth = &auth
	return policy
}

func (s *Server) listBaselines(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListBaselines(principal(request).User.TenantID, request.URL.Query().Get("assetId"), listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list baselines", err)
		return
	}
	writeList(writer, items, nextCursor)
}

// getBaseline returns one baseline including its captured snapshot. The list
// endpoint has always serialized the same fields; this exists so the snapshot
// has a declared, addressable contract instead of being an undeclared extra on
// a list response. Every repository error collapses to 404, matching getAsset
// and changeBaselineStatus: a cross-tenant id must not be distinguishable from
// a missing one.
func (s *Server) getBaseline(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetBaseline(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (s *Server) approveBaseline(writer http.ResponseWriter, request *http.Request) {
	s.changeBaselineStatus(writer, request, domain.BaselineApproved)
}

func (s *Server) rejectBaseline(writer http.ResponseWriter, request *http.Request) {
	s.changeBaselineStatus(writer, request, domain.BaselineRejected)
}

func (s *Server) retireBaseline(writer http.ResponseWriter, request *http.Request) {
	s.changeBaselineStatus(writer, request, domain.BaselineRetired)
}

func (s *Server) changeBaselineStatus(writer http.ResponseWriter, request *http.Request, status domain.BaselineStatus) {
	session := principal(request)
	baseline, err := s.repository.GetBaseline(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	if status == domain.BaselineRetired {
		if baseline.Status != domain.BaselineApproved {
			writeError(writer, request, http.StatusConflict, "INVALID_BASELINE_STATE", "只能废弃已批准基线", nil)
			return
		}
	} else if baseline.Status != domain.BaselineCandidate {
		writeError(writer, request, http.StatusConflict, "INVALID_BASELINE_STATE", "只能处理候选基线", nil)
		return
	}
	now := time.Now().UTC()
	baseline.Status = status
	if status == domain.BaselineApproved {
		baseline.ApprovedBy = session.User.ID
		baseline.ApprovedAt = &now
	}
	if err := s.repository.UpdateBaseline(baseline); err != nil {
		writeError(writer, request, http.StatusConflict, "BASELINE_UPDATE_FAILED", err.Error(), nil)
		return
	}
	s.appendAudit(request, session.User, "baseline."+strings.ToLower(string(status)), "baseline", baseline.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, baseline)
}

func (s *Server) listJobs(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListJobs(principal(request).User.TenantID, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list jobs", err)
		return
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) createJob(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		AssetID string `json:"assetId"`
		Mode    string `json:"mode"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	job, err := s.detection.Queue(session.User.TenantID, input.AssetID, input.Mode)
	if err != nil {
		writeError(writer, request, http.StatusConflict, "JOB_REJECTED", err.Error(), nil)
		return
	}
	s.appendAudit(request, session.User, "detection.run", "detection_job", job.ID, "SUCCESS", map[string]any{"assetId": input.AssetID})
	writeJSON(writer, http.StatusAccepted, job)
}

// cancelJob transitions a QUEUED or RUNNING job to CANCELLED. Jobs already in a
// terminal state (SUCCEEDED/FAILED/TIMED_OUT) are a conflict. Cancellation of a
// RUNNING job is best-effort: Process re-reads the status before its final
// write so CANCELLED wins over a late SUCCEEDED.
func (s *Server) cancelJob(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	id := request.PathValue("id")
	if _, err := s.repository.GetJob(session.User.TenantID, id); err != nil {
		s.notFound(writer, request)
		return
	}
	changed, err := s.repository.CancelJob(session.User.TenantID, id)
	if err != nil {
		s.repositoryFailure(writer, request, "cancel detection job", err)
		return
	}
	if !changed {
		writeError(writer, request, http.StatusConflict, "INVALID_JOB_STATE", "仅排队或运行中的任务可取消", nil)
		return
	}
	job, err := s.repository.GetJob(session.User.TenantID, id)
	if err != nil {
		s.repositoryFailure(writer, request, "reload cancelled detection job", err)
		return
	}
	s.appendAudit(request, session.User, "detection.cancel", "detection_job", id, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, job)
}

func (s *Server) listIncidents(writer http.ResponseWriter, request *http.Request) {
	incidents, nextCursor, err := s.repository.ListIncidents(principal(request).User.TenantID, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list incidents", err)
		return
	}
	s.updateIncidentMetrics(incidents)
	writeList(writer, incidents, nextCursor)
}

func (s *Server) updateIncidentMetrics(incidents []domain.Incident) {
	open, highRisk, critical := 0, 0, 0
	for _, incident := range incidents {
		if incident.Status == domain.IncidentClosed || incident.Status == domain.IncidentFalsePositive {
			continue
		}
		open++
		if incident.Severity == domain.SeverityHigh || incident.Severity == domain.SeverityCritical {
			highRisk++
		}
		if incident.Severity == domain.SeverityCritical {
			critical++
		}
	}
	s.metrics.Set("webmonitoring_open_incidents", float64(open), nil)
	s.metrics.Set("webmonitoring_high_risk_incidents", float64(highRisk), nil)
	s.metrics.Set("webmonitoring_critical_incidents", float64(critical), nil)
}

func (s *Server) getIncident(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetIncident(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

// addIncidentComment appends an operator's note to an incident's timeline.
//
// A comment is a timeline entry rather than a column on the incident: it is part
// of the history, and a column would keep only the most recent note. The note
// text is deliberately not copied into the audit record -- the audit says an
// incident was commented on and by whom, the timeline holds what was said.
func (s *Server) addIncidentComment(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	id := request.PathValue("id")
	incident, err := s.repository.GetIncident(session.User.TenantID, id)
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input struct {
		Note string `json:"note"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	note := strings.TrimSpace(input.Note)
	length := len([]rune(note))
	if length < 2 || length > 2000 {
		writeError(writer, request, http.StatusBadRequest, "INVALID_NOTE", "备注长度需在 2 到 2000 个字符之间", nil)
		return
	}
	event := domain.IncidentEvent{
		ID: identity.New(), TenantID: incident.TenantID, IncidentID: incident.ID,
		Type: domain.IncidentEventCommented, ActorID: session.User.ID,
		Details: map[string]any{"note": note}, CreatedAt: time.Now().UTC(),
	}
	if err := s.repository.AppendIncidentEvent(event); err != nil {
		s.repositoryFailure(writer, request, "add incident comment", err)
		return
	}
	s.appendAudit(request, session.User, "incident.comment", "incident", id, "SUCCESS", map[string]any{"length": length})
	s.broker.Publish("incident.updated", incident)
	// The note itself stays out of the notification, as it stays out of the
	// audit record: what is sent says an incident was commented on, not what was
	// said. A timeline note can carry customer detail, and a webhook destination
	// is a third party.
	s.detection.NotifyIncident(request.Context(), incident, domain.AlertReasonCommented)
	writeJSON(writer, http.StatusCreated, event)
}

// listIncidentTimeline returns one incident's history, newest first.
//
// The incident is looked up first so a missing or cross-tenant id answers 404
// rather than an empty list, which would otherwise be indistinguishable from an
// incident that has no entries yet.
func (s *Server) listIncidentTimeline(writer http.ResponseWriter, request *http.Request) {
	tenantID := principal(request).User.TenantID
	id := request.PathValue("id")
	if _, err := s.repository.GetIncident(tenantID, id); err != nil {
		s.notFound(writer, request)
		return
	}
	events, nextCursor, err := s.repository.ListIncidentEvents(tenantID, id, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list incident timeline", err)
		return
	}
	writeList(writer, events, nextCursor)
}

func (s *Server) transitionIncident(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	incident, err := s.repository.GetIncident(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	// Captured before the handler rewrites it: a timeline entry needs both ends
	// of the transition. Only scalar fields are assigned below, so a value copy
	// is enough.
	before := incident
	var input struct {
		Status     domain.IncidentStatus `json:"status"`
		OwnerID    string                `json:"ownerId"`
		Resolution string                `json:"resolution"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := detection.ValidateTransition(incident.Status, input.Status); err != nil {
		writeError(writer, request, http.StatusConflict, "INVALID_INCIDENT_TRANSITION", err.Error(), nil)
		return
	}
	if (input.Status == domain.IncidentResolved || input.Status == domain.IncidentFalsePositive || input.Status == domain.IncidentClosed) &&
		strings.TrimSpace(input.Resolution) == "" && strings.TrimSpace(incident.Resolution) == "" {
		writeError(writer, request, http.StatusBadRequest, "RESOLUTION_REQUIRED", "恢复、关闭或误报状态必须填写说明", nil)
		return
	}
	incident.Status = input.Status
	if input.OwnerID != "" {
		if _, err := s.repository.GetUser(session.User.TenantID, input.OwnerID); err != nil {
			writeError(writer, request, http.StatusBadRequest, "INVALID_OWNER", "负责人不属于当前租户", nil)
			return
		}
		incident.OwnerID = input.OwnerID
	} else if input.Status == domain.IncidentAcknowledged && incident.OwnerID == "" {
		incident.OwnerID = session.User.ID
	}
	if input.Resolution != "" {
		incident.Resolution = strings.TrimSpace(input.Resolution)
	}
	if input.Status == domain.IncidentAcknowledged && incident.AcknowledgedAt == nil {
		now := time.Now().UTC()
		incident.AcknowledgedAt = &now
	}
	if input.Status == domain.IncidentResolved || input.Status == domain.IncidentFalsePositive {
		now := time.Now().UTC()
		incident.ResolvedAt = &now
	}
	if input.Status == domain.IncidentInvestigating {
		incident.ResolvedAt = nil
	}
	if err := s.repository.UpdateIncident(incident); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "INCIDENT_UPDATE_FAILED", "无法更新事件", nil)
		return
	}
	// The timeline entry is written before the audit and its failure does not
	// fail the request: the status has already changed, so a 500 would tell the
	// caller the transition did not happen when it did. Losing one entry is the
	// lesser harm, and the audit record below still shows the action.
	if err := s.repository.AppendIncidentEvent(
		detection.IncidentTransitionEvent(before, incident, session.User, time.Now().UTC()),
	); err != nil {
		s.logger.Error("persist incident timeline entry failed", "incident_id", incident.ID, "error", err)
	}
	s.appendAudit(request, session.User, "incident.transition", "incident", incident.ID, "SUCCESS", map[string]any{"status": input.Status})
	s.broker.Publish("incident.updated", incident)
	// A status change is exactly the thing an operator watching a channel wants
	// to hear about -- an incident being acknowledged or resolved is the end of
	// the loop they were paged into. It runs after the response is decided, so a
	// notification failure cannot turn a successful transition into an error.
	s.detection.NotifyIncident(request.Context(), incident, domain.AlertReasonTransitioned)
	writeJSON(writer, http.StatusOK, incident)
}

func (s *Server) listEvidence(writer http.ResponseWriter, request *http.Request) {
	items, nextCursor, err := s.repository.ListEvidence(principal(request).User.TenantID, request.URL.Query().Get("assetId"), listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list evidence", err)
		return
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) downloadEvidence(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	evidenceID := request.PathValue("id")
	item, err := s.repository.GetEvidence(session.User.TenantID, evidenceID)
	if err != nil {
		s.appendAudit(request, session.User, "evidence.download", "evidence", evidenceID, "FAILED", map[string]any{"reason": "not_found"})
		s.notFound(writer, request)
		return
	}
	content, err := evidence.ReadVerified(s.evidence, item)
	if err != nil {
		s.appendAudit(request, session.User, "evidence.download", "evidence", item.ID, "FAILED", map[string]any{"reason": "integrity_check_failed"})
		writeError(writer, request, http.StatusNotFound, "EVIDENCE_MISSING", "证据文件不存在", nil)
		return
	}
	writer.Header().Set("Content-Type", item.MediaType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("Content-Disposition", `attachment; filename="`+item.ID+safeExtension(item.Kind)+`"`)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = writer.Write(content)
	s.appendAudit(request, session.User, "evidence.download", "evidence", item.ID, "SUCCESS", nil)
}

// signedEvidenceURL issues a short-lived download URL for an evidence object.
// S3 objects receive a presigned object URL; local objects receive a signed
// control-plane URL that still passes the ReadVerified integrity check.
func (s *Server) signedEvidenceURL(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	item, err := s.repository.GetEvidence(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	expiresAt := time.Now().UTC().Add(s.signedURLTTL)
	urlValue, err := s.signedURLFor(request.Context(), session.User.TenantID, item, expiresAt)
	if err != nil {
		s.appendAudit(request, session.User, "evidence.signed_url.issued", "evidence", item.ID, "FAILED", map[string]any{"reason": "signing_failed"})
		writeError(writer, request, http.StatusInternalServerError, "SIGNED_URL_FAILED", "无法签发短时下载链接", nil)
		return
	}
	s.appendAudit(request, session.User, "evidence.signed_url.issued", "evidence", item.ID, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, map[string]string{
		"url":       urlValue,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) signedURLFor(ctx context.Context, tenantID string, item domain.Evidence, expiresAt time.Time) (string, error) {
	if presigner, ok := s.evidence.(evidence.URLPresigner); ok {
		return presigner.PresignedGetURL(ctx, item.ObjectKey, s.signedURLTTL)
	}
	if s.signedURLSigner == nil {
		return "", errors.New("signed URLs are not configured")
	}
	message := tenantID + "|" + item.ID + "|" + strconv.FormatInt(expiresAt.Unix(), 10)
	return "/api/v1/evidence/signed/" + s.signedURLSigner.Encode(message), nil
}

// downloadSignedEvidence serves a local-store evidence object for a valid,
// unexpired signed token. The token itself is the authorization; the object is
// still verified against its size and SHA-256 metadata before streaming.
func (s *Server) downloadSignedEvidence(writer http.ResponseWriter, request *http.Request) {
	if s.signedURLSigner == nil {
		s.notFound(writer, request)
		return
	}
	message, ok := s.signedURLSigner.Decode(request.PathValue("token"))
	if !ok {
		s.notFound(writer, request)
		return
	}
	parts := strings.SplitN(message, "|", 3)
	if len(parts) != 3 {
		s.notFound(writer, request)
		return
	}
	tenantID, evidenceID := parts[0], parts[1]
	expiryUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().UTC().Unix() > expiryUnix {
		s.notFound(writer, request)
		return
	}
	item, err := s.repository.GetEvidence(tenantID, evidenceID)
	if err != nil {
		s.notFound(writer, request)
		return
	}
	content, err := evidence.ReadVerified(s.evidence, item)
	if err != nil {
		writeError(writer, request, http.StatusNotFound, "EVIDENCE_MISSING", "证据文件不存在", nil)
		return
	}
	writer.Header().Set("Content-Type", item.MediaType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("Content-Disposition", `attachment; filename="`+item.ID+safeExtension(item.Kind)+`"`)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

// alertChannelView is an alert channel as the console sees it: the stored row,
// with its endpoint redacted, plus whether its notification circuit breaker is
// currently open. The breaker state is derived rather than stored, so it is not
// a field on the domain type -- a channel that has been failing is a fact about
// the gate, not about the channel.
type alertChannelView struct {
	domain.AlertChannel
	// Suspended is true while the breaker is open: the channel is being skipped
	// rather than retried. It is the answer to "why did nobody get told".
	Suspended bool `json:"suspended"`
}

func (s *Server) listAlertChannels(writer http.ResponseWriter, request *http.Request) {
	items, err := s.repository.ListAlertChannels(principal(request).User.TenantID)
	if err != nil {
		s.repositoryFailure(writer, request, "list alert channels", err)
		return
	}
	ids := make([]string, 0, len(items))
	for index := range items {
		items[index].URL = redactEndpoint(items[index].URL)
		ids = append(ids, items[index].ID)
	}
	suspended := s.detection.SuspendedChannels(request.Context(), ids)
	views := make([]alertChannelView, 0, len(items))
	for _, channel := range items {
		views = append(views, alertChannelView{AlertChannel: channel, Suspended: suspended[channel.ID]})
	}
	writeList(writer, views, "")
}

func (s *Server) listAlertDeliveries(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	incidentID := request.URL.Query().Get("incidentId")
	if incidentID != "" {
		if _, err := s.repository.GetIncident(session.User.TenantID, incidentID); err != nil {
			s.notFound(writer, request)
			return
		}
	}
	items, nextCursor, err := s.repository.ListAlertDeliveries(session.User.TenantID, incidentID, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list alert deliveries", err)
		return
	}
	writeList(writer, items, nextCursor)
}

func (s *Server) reportSummary(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	from, to, err := reportRange(request)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_REPORT_RANGE", err.Error(), nil)
		return
	}
	assetID := request.URL.Query().Get("assetId")
	if assetID != "" {
		if _, err := s.repository.GetAsset(session.User.TenantID, assetID); err != nil {
			s.notFound(writer, request)
			return
		}
	}
	severityFilter := strings.ToUpper(strings.TrimSpace(request.URL.Query().Get("severity")))
	if severityFilter != "" && !validSeverity(domain.Severity(severityFilter)) {
		writeError(writer, request, http.StatusBadRequest, "INVALID_SEVERITY", "风险等级无效", nil)
		return
	}
	assets, _, err := s.repository.ListAssets(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list report assets", err)
		return
	}
	verifiedAssets := 0
	assetIDs := make(map[string]struct{})
	for _, asset := range assets {
		if assetID != "" && asset.ID != assetID {
			continue
		}
		if !withinRange(asset.CreatedAt, from, to) {
			continue
		}
		assetIDs[asset.ID] = struct{}{}
		if asset.VerificationStatus == domain.VerificationVerified {
			verifiedAssets++
		}
	}
	jobs, _, err := s.repository.ListJobs(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list report jobs", err)
		return
	}
	jobCount, successfulJobs, failedJobs := 0, 0, 0
	for _, job := range jobs {
		if _, ok := assetIDs[job.AssetID]; !ok || !withinRange(job.CreatedAt, from, to) {
			continue
		}
		jobCount++
		if job.Status == domain.JobSucceeded {
			successfulJobs++
		}
		if job.Status == domain.JobFailed || job.Status == domain.JobTimedOut {
			failedJobs++
		}
	}
	incidents, _, err := s.repository.ListIncidents(session.User.TenantID, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list report incidents", err)
		return
	}
	severityCounts := map[string]int{}
	incidentIDs := make(map[string]struct{})
	mttd, mtta, mttr := make([]float64, 0), make([]float64, 0), make([]float64, 0)
	for _, incident := range incidents {
		if _, ok := assetIDs[incident.AssetID]; !ok || !withinRange(incident.LastSeenAt, from, to) {
			continue
		}
		if severityFilter != "" && string(incident.Severity) != severityFilter {
			continue
		}
		severityCounts[string(incident.Severity)]++
		incidentIDs[incident.ID] = struct{}{}
		if value := incident.CreatedAt.Sub(incident.FirstSeenAt).Seconds(); value >= 0 {
			mttd = append(mttd, value)
		}
		if incident.AcknowledgedAt != nil {
			if value := incident.AcknowledgedAt.Sub(incident.FirstSeenAt).Seconds(); value >= 0 {
				mtta = append(mtta, value)
			}
		}
		if incident.ResolvedAt != nil {
			if value := incident.ResolvedAt.Sub(incident.FirstSeenAt).Seconds(); value >= 0 {
				mttr = append(mttr, value)
			}
		}
	}
	deliveries, _, err := s.repository.ListAlertDeliveries(session.User.TenantID, "", store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "list report deliveries", err)
		return
	}
	alertDeliveries := 0
	for _, delivery := range deliveries {
		if _, ok := incidentIDs[delivery.IncidentID]; ok {
			alertDeliveries++
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"assets": len(assetIDs), "verifiedAssets": verifiedAssets,
		"detectionJobs": jobCount, "successfulJobs": successfulJobs, "failedJobs": failedJobs,
		"incidents": len(incidentIDs), "severityCounts": severityCounts, "alertDeliveries": alertDeliveries,
		"mttdSeconds": averageSeconds(mttd), "mttaSeconds": averageSeconds(mtta), "mttrSeconds": averageSeconds(mttr),
	})
}

func reportRange(request *http.Request) (time.Time, time.Time, error) {
	var from, to time.Time
	for name, target := range map[string]*time.Time{"from": &from, "to": &to} {
		value := strings.TrimSpace(request.URL.Query().Get(name))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%s must be RFC3339", name)
		}
		*target = parsed.UTC()
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return time.Time{}, time.Time{}, errors.New("from must not be after to")
	}
	return from, to, nil
}

func withinRange(value, from, to time.Time) bool {
	return (from.IsZero() || !value.Before(from)) && (to.IsZero() || value.Before(to))
}

func averageSeconds(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func validSeverity(value domain.Severity) bool {
	return value == domain.SeverityInfo || value == domain.SeverityLow || value == domain.SeverityMedium ||
		value == domain.SeverityHigh || value == domain.SeverityCritical
}

// alertChannelInput is the request body shared by create and update.
type alertChannelInput struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Recipients string `json:"recipients"`
	Secret     string `json:"secret"`
	Enabled    bool   `json:"enabled"`
}

// validateAlertChannelInput applies every rule that decides whether a channel
// may be stored. It exists so create and update cannot drift: the SSRF guard
// below is the control that keeps a channel from pointing at the internal
// network, and a second, subtly different copy of it on the update path would
// be a hole that only the update path has.
//
// It returns the sanitized input and whether the caller may proceed; the error
// response is already written when it returns false.
func (s *Server) validateAlertChannelInput(writer http.ResponseWriter, request *http.Request, input alertChannelInput) (alertChannelInput, bool) {
	input.Type = strings.ToUpper(input.Type)
	switch input.Type {
	case "WEBHOOK", "WECHAT_WORK", "DINGTALK", "EMAIL":
	default:
		writeError(writer, request, http.StatusBadRequest, "INVALID_CHANNEL", "渠道类型必须是 WEBHOOK、WECHAT_WORK、DINGTALK 或 EMAIL", nil)
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_CHANNEL", "渠道名称不能为空", nil)
		return input, false
	}
	if input.Type == "EMAIL" {
		recipients, ok := normalizeRecipients(input.Recipients)
		if !ok {
			writeError(writer, request, http.StatusBadRequest, "INVALID_CHANNEL", "邮件渠道必须提供有效的收件人地址", nil)
			return input, false
		}
		input.Recipients = recipients
		input.URL = ""
		return input, true
	}
	// A webhook-ish type carries no recipients; clear whatever was sent so a
	// stale address list cannot be stored against a channel that never reads it.
	input.Recipients = ""
	if _, _, err := s.guard.Validate(request.Context(), input.URL); err != nil {
		writeError(writer, request, http.StatusBadRequest, "UNSAFE_WEBHOOK", "通知地址不符合安全策略", nil)
		return input, false
	}
	return input, true
}

func (s *Server) createAlertChannel(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input alertChannelInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	input, ok := s.validateAlertChannelInput(writer, request, input)
	if !ok {
		return
	}
	now := time.Now().UTC()
	channel := domain.AlertChannel{
		ID: identity.New(), TenantID: session.User.TenantID, Name: input.Name,
		Type: input.Type, URL: input.URL, Recipients: input.Recipients, Secret: input.Secret, Enabled: input.Enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.CreateAlertChannel(channel); err != nil {
		writeError(writer, request, http.StatusConflict, "CHANNEL_EXISTS", err.Error(), nil)
		return
	}
	s.appendAudit(request, session.User, "alert_channel.create", "alert_channel", channel.ID, "SUCCESS", nil)
	channel.Secret = ""
	writeJSON(writer, http.StatusCreated, channel)
}

// updateAlertChannel rewrites a channel in place. The row keeps its id: the
// stored credentials are bound to it through their associated data, so a
// recreated row would never decrypt again.
func (s *Server) updateAlertChannel(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	existing, err := s.repository.GetAlertChannel(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var input alertChannelInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	input, ok := s.validateAlertChannelInput(writer, request, input)
	if !ok {
		return
	}
	// An empty secret means "leave the stored one alone". The client never
	// receives the plaintext (every response clears it), so it cannot echo the
	// current value back, and treating empty as a deliberate erase would wipe
	// the credential on any edit that only renamed the channel.
	secret := input.Secret
	if secret == "" {
		secret = existing.Secret
	}
	channel := existing
	channel.Name = input.Name
	channel.Type = input.Type
	channel.URL = input.URL
	channel.Recipients = input.Recipients
	channel.Secret = secret
	channel.Enabled = input.Enabled
	channel.UpdatedAt = time.Now().UTC()
	if err := s.repository.UpdateAlertChannel(channel); err != nil {
		s.notFound(writer, request)
		return
	}
	s.appendAudit(request, session.User, "alert_channel.update", "alert_channel", channel.ID, "SUCCESS", nil)
	channel.Secret = ""
	writeJSON(writer, http.StatusOK, channel)
}

// deleteAlertChannel removes the configuration, not the history. Deliveries
// already recorded against it stay: they are the evidence that an alert was
// sent, and destroying them would erase the audit trail the console shows.
func (s *Server) deleteAlertChannel(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	id := request.PathValue("id")
	if err := s.repository.DeleteAlertChannel(session.User.TenantID, id); err != nil {
		s.notFound(writer, request)
		return
	}
	s.appendAudit(request, session.User, "alert_channel.delete", "alert_channel", id, "SUCCESS", nil)
	writer.WriteHeader(http.StatusNoContent)
}

// testAlertChannel sends one throwaway message down a channel so the operator
// who just finished configuring it can find out whether it works.
//
// The audit record is written before the attempt and its result recorded, not
// skipped on failure: an outbound message left this system, addressed to a
// destination the operator named, and that is exactly the kind of action the
// audit log exists to hold. A failed attempt is still an attempt.
//
// The response carries the delivery error text because the operator is the one
// who can act on it -- "notification endpoint returned 401" is the whole answer
// to why the test failed. It is the redacted form, the same string the delivery
// table would show.
func (s *Server) testAlertChannel(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	id := request.PathValue("id")
	err := s.detection.TestChannel(request.Context(), session.User.TenantID, id)
	if err != nil {
		outcome := "SUCCESS"
		if !errors.Is(err, store.ErrNotFound) {
			outcome = "FAILURE"
		}
		detail := map[string]any{"error": security.RedactString(err.Error())}
		s.appendAudit(request, session.User, "alert_channel.test", "alert_channel", id, outcome, detail)
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(writer, request)
			return
		}
		writeError(writer, request, http.StatusBadGateway, "CHANNEL_TEST_FAILED", security.RedactString(err.Error()), nil)
		return
	}
	s.appendAudit(request, session.User, "alert_channel.test", "alert_channel", id, "SUCCESS", nil)
	writeJSON(writer, http.StatusOK, map[string]string{"status": "DELIVERED"})
}

// normalizeRecipients validates and canonicalizes a comma-separated recipient
// list for EMAIL alert channels. It returns the normalized list and whether
// every entry is a plausible email address.
func normalizeRecipients(raw string) (string, bool) {
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		at := strings.Index(part, "@")
		if at <= 0 || at == len(part)-1 || strings.ContainsAny(part, " \t\r\n") {
			return "", false
		}
		normalized = append(normalized, part)
	}
	if len(normalized) == 0 {
		return "", false
	}
	return strings.Join(normalized, ","), true
}

func (s *Server) listAudits(writer http.ResponseWriter, request *http.Request) {
	filter, err := parseAuditFilter(request)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_FILTER", err.Error(), nil)
		return
	}
	items, nextCursor, err := s.repository.ListAudits(principal(request).User.TenantID, filter, listOptions(request))
	if err != nil {
		s.repositoryFailure(writer, request, "list audit events", err)
		return
	}
	writeList(writer, items, nextCursor)
}

// parseAuditFilter reads the optional audit narrowing query parameters. A
// malformed value is a 400 rather than a silently empty result, and a reversed
// time range is rejected for the same reason: the caller asked a question the
// server can answer precisely, so answering it wrongly is worse than refusing.
func parseAuditFilter(request *http.Request) (store.AuditFilter, error) {
	query := request.URL.Query()
	filter := store.AuditFilter{
		ActorID:      query.Get("actorId"),
		Action:       query.Get("action"),
		ResourceType: query.Get("resourceType"),
		ResourceID:   query.Get("resourceId"),
		Result:       query.Get("result"),
	}
	// Postgres casts this to uuid (see PostgresRepository.ListAudits), so a
	// non-uuid reaches the driver as 22P02 and surfaces as a 500.
	if filter.ActorID != "" && !looksLikeUUID(filter.ActorID) {
		return store.AuditFilter{}, errors.New("actorId 必须是 UUID")
	}
	if filter.Result != "" && !validAuditResults[filter.Result] {
		return store.AuditFilter{}, errors.New("result 只能是 SUCCESS、FAILED 或 PARTIAL")
	}
	var err error
	if filter.From, err = parseAuditTime(query.Get("from")); err != nil {
		return store.AuditFilter{}, fmt.Errorf("from 必须是 RFC3339 时间: %w", err)
	}
	if filter.To, err = parseAuditTime(query.Get("to")); err != nil {
		return store.AuditFilter{}, fmt.Errorf("to 必须是 RFC3339 时间: %w", err)
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		return store.AuditFilter{}, errors.New("to 不能早于 from")
	}
	return filter, nil
}

func parseAuditTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

// looksLikeUUID validates the canonical 8-4-4-4-12 hex shape. The codebase has
// no uuid parser (identity.New only generates), and this is the one place a
// caller-supplied id is handed to Postgres as a cast rather than compared as
// text.
func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// validAuditResults mirrors the values the writers produce: handlers emit
// SUCCESS or FAILED, and the retention sweep emits PARTIAL when some tenants
// were skipped.
var validAuditResults = map[string]bool{"SUCCESS": true, "FAILED": true, "PARTIAL": true}

// runRetentionMaintenance is the authorized manual trigger. It delegates to the
// same maintenance.Runner the background loop uses, so a dry run here reports
// exactly what the scheduled pass would do.
func (s *Server) runRetentionMaintenance(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Execute bool `json:"execute"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	result, err := s.retention.RunTenant(request.Context(), session.User.TenantID, input.Execute)
	if err != nil {
		s.repositoryFailure(writer, request, "run retention maintenance", err)
		return
	}
	action := "maintenance.retention.dry_run"
	if input.Execute {
		action = "maintenance.retention.execute"
	}
	s.appendAudit(request, session.User, action, "retention", session.User.TenantID, "SUCCESS", map[string]any{
		"dryRun": result.DryRun, "expiredEvidence": result.ExpiredEvidence,
		"deletedEvidence": result.DeletedEvidence, "protectedEvidence": result.ProtectedEvidence,
		"failedEvidence": result.FailedEvidence, "deletedAuditEvents": result.DeletedAuditEvents,
		"failureReasons": result.FailureReasons,
	})
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) downloadReport(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	incident, err := s.repository.GetIncident(session.User.TenantID, request.PathValue("incidentId"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	summary, err := archive.Create("incident.json")
	if err != nil {
		s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "archive_create_failed"})
		writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法创建报告", nil)
		return
	}
	if err := json.NewEncoder(summary).Encode(incident); err != nil {
		_ = archive.Close()
		s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "summary_write_failed"})
		writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法写入报告摘要", nil)
		return
	}
	for _, evidenceID := range incident.EvidenceIDs {
		item, getErr := s.repository.GetEvidence(session.User.TenantID, evidenceID)
		if getErr != nil {
			_ = archive.Close()
			s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "evidence_metadata_missing", "evidenceId": evidenceID})
			writeError(writer, request, http.StatusFailedDependency, "REPORT_INCOMPLETE", "报告所需证据元数据缺失", map[string]any{"evidenceId": evidenceID})
			return
		}
		content, openErr := evidence.ReadVerified(s.evidence, item)
		if openErr != nil {
			_ = archive.Close()
			s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "evidence_integrity_failed", "evidenceId": evidenceID})
			writeError(writer, request, http.StatusFailedDependency, "REPORT_INCOMPLETE", "报告所需证据文件缺失或完整性校验失败", map[string]any{"evidenceId": evidenceID})
			return
		}
		entry, createErr := archive.Create("evidence/" + item.ID + safeExtension(item.Kind))
		if createErr != nil {
			_ = archive.Close()
			s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "archive_entry_failed", "evidenceId": evidenceID})
			writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法创建报告证据条目", nil)
			return
		}
		if _, copyErr := entry.Write(content); copyErr != nil {
			_ = archive.Close()
			s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "evidence_read_failed", "evidenceId": evidenceID})
			writeError(writer, request, http.StatusFailedDependency, "REPORT_INCOMPLETE", "报告证据读取失败", map[string]any{"evidenceId": evidenceID})
			return
		}
	}
	if err := archive.Close(); err != nil {
		s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "FAILED", map[string]any{"reason": "archive_close_failed"})
		writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法完成报告", nil)
		return
	}
	s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "SUCCESS", nil)
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Length", strconv.Itoa(buffer.Len()))
	writer.Header().Set("Content-Disposition", `attachment; filename="incident-`+incident.ID+`.zip"`)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(buffer.Bytes())
}

func (s *Server) streamEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, request, http.StatusNotImplemented, "SSE_UNSUPPORTED", "服务器不支持事件流", nil)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	channel, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()
	_, _ = fmt.Fprint(writer, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case payload := <-channel:
			_, _ = fmt.Fprintf(writer, "event: update\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(writer, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// checkVerification dispatches the VERIFY action to the method selected at
// asset creation. Every method compares the SHA-256 of the published value
// against VerificationTokenHash using a constant-time comparison, and all
// network access for FILE/META goes through the SSRF-guarded checker.
func (s *Server) checkVerification(ctx context.Context, asset domain.Asset) error {
	switch assetMethod(asset) {
	case domain.VerificationDNS:
		return s.checkDNSVerification(ctx, asset)
	case domain.VerificationMeta:
		return s.checkMetaVerification(ctx, asset)
	default:
		return s.checkFileVerification(ctx, asset)
	}
}

func (s *Server) checkFileVerification(ctx context.Context, asset domain.Asset) error {
	base, err := url.Parse(asset.URL)
	if err != nil {
		return err
	}
	base.Path = "/.well-known/webmonitoring-verification.txt"
	base.RawQuery = ""
	base.Fragment = ""
	temp := asset
	temp.URL = base.String()
	checker := detection.NewChecker(s.guard)
	result, err := checker.Check(ctx, temp, domain.MonitorPolicy{
		TimeoutSeconds: 15, MaxResponseBytes: 4096,
	})
	if err != nil {
		return err
	}
	if result.Snapshot.StatusCode != http.StatusOK || !s.verificationMatches(asset, string(result.RawHTML)) {
		return errors.New("verification token mismatch")
	}
	return nil
}

func (s *Server) checkMetaVerification(ctx context.Context, asset domain.Asset) error {
	checker := detection.NewChecker(s.guard)
	result, err := checker.Check(ctx, asset, domain.MonitorPolicy{
		TimeoutSeconds: 15, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		return err
	}
	if result.Snapshot.StatusCode != http.StatusOK {
		return errors.New("verification target unavailable")
	}
	if value := detection.FindMetaVerification(result.RawHTML); value == "" || !s.verificationMatches(asset, value) {
		return errors.New("verification token mismatch")
	}
	return nil
}

func (s *Server) checkDNSVerification(ctx context.Context, asset domain.Asset) error {
	host, err := verificationHostname(asset.URL)
	if err != nil {
		return err
	}
	records, err := s.guard.Resolver.LookupTXT(ctx, "_webmonitoring-verify."+host)
	if err != nil {
		return err
	}
	for _, record := range records {
		if s.verificationMatches(asset, record) {
			return nil
		}
	}
	return errors.New("verification token mismatch")
}

// verificationMatches reports whether a published value matches the issued
// token, comparing hashes in constant time. When VerificationTokenHash is empty
// it falls back to hashing the in-memory VerificationToken (legacy path).
func (s *Server) verificationMatches(asset domain.Asset, candidate string) bool {
	expected := asset.VerificationTokenHash
	if expected == "" {
		expected = fmt.Sprintf("%x", sha256.Sum256([]byte(asset.VerificationToken)))
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(candidate)))
	return subtle.ConstantTimeCompare([]byte(fmt.Sprintf("%x", hash)), []byte(expected)) == 1
}

func assetMethod(asset domain.Asset) domain.VerificationMethod {
	if asset.VerificationMethod == "" {
		return domain.VerificationFile
	}
	return asset.VerificationMethod
}

func verificationHostname(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return parsed.Hostname(), nil
}

// verificationInstructions builds the ISSUE response for a verification method.
// FILE keeps the legacy "path"/"content" keys so the existing console flow is
// unchanged; DNS and META add their own keys.
func verificationInstructions(asset domain.Asset, value string) map[string]string {
	result := map[string]string{
		"method":  string(assetMethod(asset)),
		"content": value,
	}
	switch assetMethod(asset) {
	case domain.VerificationDNS:
		if host, err := verificationHostname(asset.URL); err == nil {
			result["record"] = "_webmonitoring-verify." + host
			result["recordType"] = "TXT"
		}
	case domain.VerificationMeta:
		result["tag"] = `<meta name="webmonitoring-verification" content="` + value + `">`
	default:
		result["path"] = "/.well-known/webmonitoring-verification.txt"
	}
	return result
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(sessionCookie)
		if err != nil {
			writeError(writer, request, http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录", nil)
			return
		}
		session, ok := s.auth.Get(request.Context(), cookie.Value)
		if !ok {
			writeError(writer, request, http.StatusUnauthorized, "SESSION_EXPIRED", "会话已失效", nil)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalKey{}, session)))
	})
}

// rateLimit bounds how many requests one tenant may make per window, by class.
//
// It sits inside authenticate, not outside it, because the counter is keyed by
// tenant and the tenant is only known once the session is resolved. That
// ordering also means an unauthenticated flood never reaches the limiter, which
// is correct: those requests are answered by authenticate, and counting them
// here would let an anonymous caller exhaust a tenant's allowance.
//
// A refusal is 429 with Retry-After, not 403. The caller is authenticated and
// authorized; only the rate is the problem, and a client that backs off should
// be told to rather than being made to look at a permissions error.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		session := principal(request)
		class := ratelimit.Classify(request.Method, request.URL.Path)
		allowed, err := s.rateLimiter.Allow(request.Context(), session.User.TenantID, class)
		if err != nil {
			// Allow already fails open; the error is logged so a broken limiter
			// is visible rather than silently doing nothing.
			s.logger.Warn("rate limiter unavailable; allowing request", "error", err)
		}
		if !allowed {
			retryAfter := s.rateLimiter.RetryAfter()
			writer.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			s.metrics.Inc("webmonitoring_rate_limited_total", map[string]string{
				"class": string(class),
			})
			writeError(writer, request, http.StatusTooManyRequests, "RATE_LIMITED",
				"请求过于频繁，请稍后重试", map[string]any{"retryAfterSeconds": int(retryAfter.Seconds())})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
			session := principal(request)
			provided := request.Header.Get("X-CSRF-Token")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
				writeError(writer, request, http.StatusForbidden, "CSRF_INVALID", "CSRF 校验失败", nil)
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) requireRoles(
	handler http.HandlerFunc,
	roles ...domain.Role,
) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		current := principal(request).User.Role
		// SUPER_ADMIN is a superset within its own tenant: the bootstrap account
		// must be able to administer the default tenant in addition to tenants.
		if current == domain.RoleSuperAdmin {
			handler(writer, request)
			return
		}
		for _, role := range roles {
			if current == role {
				handler(writer, request)
				return
			}
		}
		writeError(writer, request, http.StatusForbidden, "FORBIDDEN", "当前角色无权执行该操作", nil)
	}
}

func (s *Server) requireSuperAdmin(handler http.HandlerFunc) http.HandlerFunc {
	return s.requireRoles(handler, domain.RoleSuperAdmin)
}

// applyCORS echoes the request's Origin when it is on the allowlist, and reports
// whether it did. Echoing rather than answering "*" is required because every
// authenticated call carries a session cookie: the browser will not send
// credentials to a wildcard origin. Vary: Origin is always set so a shared cache
// cannot serve one origin's response to another.
func (s *Server) applyCORS(writer http.ResponseWriter, request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	writer.Header().Add("Vary", "Origin")
	if _, ok := s.allowedOrigins[origin]; !ok {
		return false
	}
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Set("Access-Control-Allow-Credentials", "true")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, X-Request-ID")
	// The signed-URL countdown in the console reads this header off the response,
	// so a cross-origin console has to be told it may.
	writer.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
	writer.Header().Set("Access-Control-Max-Age", "600")
	return true
}

func (s *Server) trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		captured := &metricsResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		// The span wraps the whole request so downstream work (detection,
		// notification) can attach itself to the same trace. When tracing is
		// disabled the span is a no-op and the header below falls back to a
		// locally generated request ID, exactly as before.
		ctx, span := apiTracer.Start(request.Context(), spanName(request.Method, request.URL.Path),
			trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		traceID := request.Header.Get("X-Request-ID")
		if traceID == "" {
			if spanContext := span.SpanContext(); spanContext.IsValid() {
				// Reuse the trace ID so the X-Request-ID a caller sees, the
				// audit record, and the exported trace all agree.
				traceID = spanContext.TraceID().String()
			} else {
				traceID = identity.New()
			}
			span.SetAttributes(attribute.String("http.request_id", traceID))
		}
		writer.Header().Set("X-Request-ID", traceID)
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// HSTS is only meaningful over TLS: sending it on a plaintext response
		// would pin the browser to https for a host that does not serve it. It
		// follows the session cookie's Secure flag, which is already the switch
		// that says "this deployment is behind TLS termination".
		if s.sessionSecure {
			writer.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// A preflight is answered here and never reaches a handler: the browser
		// sends it without credentials, so routing it through auth would answer
		// 401 and the real request would never be attempted.
		if request.Method == http.MethodOptions && request.Header.Get("Access-Control-Request-Method") != "" {
			if s.applyCORS(writer, request) {
				writer.WriteHeader(http.StatusNoContent)
				return
			}
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		s.applyCORS(writer, request)
		next.ServeHTTP(captured, request.WithContext(context.WithValue(ctx, traceKey{}, traceID)))
		span.SetAttributes(attribute.Int("http.response.status_code", captured.status))
		if captured.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(captured.status))
		}
		s.metrics.Inc("webmonitoring_http_requests_total", map[string]string{
			"method": request.Method, "status": strconv.Itoa(captured.status),
		})
		s.metrics.ObserveDuration("webmonitoring_http_request_duration_seconds", time.Since(started), map[string]string{
			"method": request.Method,
		})
	})
}

// spanName builds the exported span name for a request.
//
// The path is used as-is except for the one route whose path segment is itself a
// credential: a signed evidence token is a bearer token, and exporting it as a
// span name would write it into the tracing backend. That segment is replaced by
// its route placeholder.
func spanName(method, path string) string {
	if strings.HasPrefix(path, "/api/v1/evidence/signed/") {
		return method + " /api/v1/evidence/signed/{token}"
	}
	return method + " " + path
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricsResponseWriter) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(value)
}

func (w *metricsResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) appendAudit(
	request *http.Request,
	user domain.User,
	action, resourceType, resourceID, result string,
	details map[string]any,
) {
	if err := s.repository.AppendAudit(domain.AuditEvent{
		ID: identity.New(), TenantID: user.TenantID, ActorID: user.ID, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Result: result,
		TraceID: traceID(request), Details: details, CreatedAt: time.Now().UTC(),
	}); err != nil {
		s.logger.Error("persist audit event failed", "action", action, "resource_type", resourceType, "resource_id", resourceID, "error", err)
	}
}

func (s *Server) notFound(writer http.ResponseWriter, request *http.Request) {
	writeError(writer, request, http.StatusNotFound, "NOT_FOUND", "资源不存在", nil)
}

func (s *Server) repositoryFailure(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	s.logger.Error("repository operation failed", "operation", operation, "error", err)
	writeError(writer, request, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", "暂时无法读取业务数据", nil)
}

func principal(request *http.Request) auth.Session {
	session, _ := request.Context().Value(principalKey{}).(auth.Session)
	return session
}

func traceID(request *http.Request) string {
	value, _ := request.Context().Value(traceKey{}).(string)
	return value
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func sessionResponse(session auth.Session) map[string]any {
	return map[string]any{
		"user": session.User, "tenant": session.Tenant, "csrfToken": session.CSRFToken, "expiresAt": session.ExpiresAt,
	}
}

func defaultPolicy(asset domain.Asset) domain.MonitorPolicy {
	now := time.Now().UTC()
	return domain.MonitorPolicy{
		ID: identity.New(), TenantID: asset.TenantID, AssetID: asset.ID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 1800, TimeoutSeconds: 30,
		MaxResponseBytes: 5 * 1024 * 1024, IgnoreSelectors: []string{},
		TrustedDomains: []string{}, CreatedAt: now, UpdatedAt: now,
	}
}

func canonicalURL(value *url.URL) string {
	value.Fragment = ""
	if (value.Scheme == "http" && value.Port() == "80") || (value.Scheme == "https" && value.Port() == "443") {
		value.Host = value.Hostname()
	}
	return value.String()
}

func redactEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.Path != "" && parsed.Path != "/" {
		parsed.Path = "/..."
	}
	return parsed.String()
}

func validAssetType(value domain.AssetType) bool {
	return value == domain.AssetOfficial || value == domain.AssetBusiness ||
		value == domain.AssetLogin || value == domain.AssetOther
}

func normalizeSlug(value string) (string, bool) {
	slug := strings.ToLower(strings.TrimSpace(value))
	if slug == "" {
		return "", false
	}
	for _, r := range slug {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return "", false
		}
	}
	return slug, true
}

func safeExtension(kind string) string {
	switch {
	case strings.HasSuffix(kind, ".html"):
		return ".html"
	case strings.HasSuffix(kind, ".json"):
		return ".json"
	case strings.HasSuffix(kind, ".png"):
		return ".png"
	default:
		return ".bin"
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 1024*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_JSON", "请求数据格式错误", map[string]any{"reason": err.Error()})
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeList[T any](writer http.ResponseWriter, items []T, nextCursor string) {
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func writeError(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	code, message string,
	details map[string]any,
) {
	writeJSON(writer, status, map[string]any{
		"code": code, "message": security.RedactString(message), "traceId": traceID(request),
		"details": security.RedactAny(details),
	})
}

func parseLimit(request *http.Request) int {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

func parseCursor(request *http.Request) string {
	return request.URL.Query().Get("cursor")
}

// listOptions builds the pagination options for a list handler. The tenant
// filter is always derived from the authenticated principal, never the request.
func listOptions(request *http.Request) store.ListOptions {
	return store.ListOptions{Limit: parseLimit(request), Cursor: parseCursor(request)}
}
