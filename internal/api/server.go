package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/events"
	"webmonitoring/internal/evidence"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

const (
	sessionCookie = "webmonitoring_session"
	defaultTenant = "00000000-0000-4000-8000-000000000001"
)

type principalKey struct{}
type traceKey struct{}

type Server struct {
	repository    store.Repository
	auth          *auth.Service
	detection     *detection.Service
	evidence      evidence.Store
	guard         *security.URLGuard
	broker        *events.Broker
	logger        *slog.Logger
	sessionSecure bool
	metrics       *metrics.Registry
	readiness     func(context.Context) map[string]error
	handler       http.Handler
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
		repository:    repository,
		auth:          authService,
		detection:     detectionService,
		evidence:      evidenceStore,
		guard:         guard,
		broker:        broker,
		logger:        logger,
		sessionSecure: sessionSecure,
		metrics:       metrics.New(),
	}
	server.handler = server.routes()
	return server
}

func (s *Server) Metrics() *metrics.Registry {
	return s.metrics
}

func (s *Server) SetReadiness(check func(context.Context) map[string]error) {
	s.readiness = check
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

	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/v1/auth/session", s.session)
	protected.HandleFunc("DELETE /api/v1/auth/session", s.logout)
	protected.HandleFunc("GET /api/v1/dashboard", s.dashboard)
	protected.HandleFunc("GET /api/v1/assets", s.listAssets)
	protected.HandleFunc("POST /api/v1/assets", s.requireRoles(s.createAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/assets/{id}", s.getAsset)
	protected.HandleFunc("PATCH /api/v1/assets/{id}", s.requireRoles(s.updateAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/assets/{id}/verification", s.requireRoles(s.verifyAsset, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/policies", s.listPolicies)
	protected.HandleFunc("PUT /api/v1/policies/{assetId}", s.requireRoles(s.updatePolicy, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/baselines", s.listBaselines)
	protected.HandleFunc("POST /api/v1/baselines/{id}/approve", s.requireRoles(s.approveBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/baselines/{id}/reject", s.requireRoles(s.rejectBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("POST /api/v1/baselines/{id}/retire", s.requireRoles(s.retireBaseline, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/detection-jobs", s.listJobs)
	protected.HandleFunc("POST /api/v1/detection-jobs", s.requireRoles(s.createJob, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/incidents", s.listIncidents)
	protected.HandleFunc("GET /api/v1/incidents/{id}", s.getIncident)
	protected.HandleFunc("POST /api/v1/incidents/{id}/transition", s.requireRoles(s.transitionIncident, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/evidence", s.listEvidence)
	protected.HandleFunc("GET /api/v1/evidence/{id}/download", s.downloadEvidence)
	protected.HandleFunc("GET /api/v1/alert-channels", s.listAlertChannels)
	protected.HandleFunc("POST /api/v1/alert-channels", s.requireRoles(s.createAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/alert-deliveries", s.listAlertDeliveries)
	protected.HandleFunc("GET /api/v1/audit-events", s.listAudits)
	protected.HandleFunc("GET /api/v1/reports/summary", s.reportSummary)
	protected.HandleFunc("GET /api/v1/reports/{incidentId}", s.downloadReport)
	protected.HandleFunc("GET /api/v1/events/stream", s.streamEvents)

	root.Handle("/api/v1/", s.authenticate(s.csrf(protected)))
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	session, err := s.auth.Login(defaultTenant, strings.TrimSpace(input.Username), input.Password)
	if err != nil {
		writeError(writer, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码错误", nil)
		return
	}
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
	s.auth.Delete(session.ID)
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.sessionSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	s.appendAudit(request, session.User, "auth.logout", "session", session.ID, "SUCCESS", nil)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) dashboard(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	assets := s.repository.ListAssets(session.User.TenantID)
	incidents := s.repository.ListIncidents(session.User.TenantID)
	jobs := s.repository.ListJobs(session.User.TenantID)
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
	items := s.repository.ListAssets(principal(request).User.TenantID)
	writeList(writer, items)
}

func (s *Server) getAsset(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetAsset(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func (s *Server) createAsset(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Name    string           `json:"name"`
		URL     string           `json:"url"`
		Type    domain.AssetType `json:"type"`
		OwnerID string           `json:"ownerId"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	parsed, _, err := s.guard.Validate(request.Context(), input.URL)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "UNSAFE_TARGET", "目标 URL 不符合安全策略", nil)
		return
	}
	if strings.TrimSpace(input.Name) == "" || !validAssetType(input.Type) {
		writeError(writer, request, http.StatusBadRequest, "INVALID_ASSET", "资产名称或类型无效", nil)
		return
	}
	now := time.Now().UTC()
	asset := domain.Asset{
		ID:                 identity.New(),
		TenantID:           session.User.TenantID,
		Name:               strings.TrimSpace(input.Name),
		URL:                canonicalURL(parsed),
		Type:               input.Type,
		OwnerID:            input.OwnerID,
		Enabled:            false,
		VerificationStatus: domain.VerificationPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if asset.OwnerID == "" {
		asset.OwnerID = session.User.ID
	} else if _, err := s.repository.GetUser(session.User.TenantID, asset.OwnerID); err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_OWNER", "负责人不属于当前租户", nil)
		return
	}
	if err := s.repository.CreateAsset(asset); err != nil {
		writeError(writer, request, http.StatusConflict, "ASSET_EXISTS", "该 URL 已经登记", nil)
		return
	}
	policy := defaultPolicy(asset)
	if err := s.repository.UpsertPolicy(policy); err != nil {
		writeError(writer, request, http.StatusInternalServerError, "POLICY_CREATE_FAILED", "无法创建默认监测策略", nil)
		return
	}
	s.appendAudit(request, session.User, "asset.create", "asset", asset.ID, "SUCCESS", map[string]any{"url": asset.URL})
	writeJSON(writer, http.StatusCreated, asset)
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
		token := "webmonitoring-verification=" + identity.New()
		expires := now.Add(30 * time.Minute)
		asset.VerificationToken = ""
		asset.VerificationTokenHash = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
		asset.VerificationIssuedAt = &now
		asset.VerificationExpiresAt = &expires
		asset.VerificationStatus = domain.VerificationPending
		asset.UpdatedAt = now
		if err := s.repository.UpdateAsset(asset); err != nil {
			writeError(writer, request, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "无法保存验证状态", nil)
			return
		}
		s.appendAudit(request, session.User, "asset.verification.issue", "asset", asset.ID, "SUCCESS", nil)
		writeJSON(writer, http.StatusOK, map[string]string{
			"path": "/.well-known/webmonitoring-verification.txt", "content": token,
		})
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

func (s *Server) listPolicies(writer http.ResponseWriter, request *http.Request) {
	writeList(writer, s.repository.ListPolicies(principal(request).User.TenantID))
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
		HTTPIntervalSeconds    int        `json:"httpIntervalSeconds"`
		BrowserIntervalSeconds int        `json:"browserIntervalSeconds"`
		TimeoutSeconds         int        `json:"timeoutSeconds"`
		MaxResponseBytes       int64      `json:"maxResponseBytes"`
		IgnoreSelectors        []string   `json:"ignoreSelectors"`
		TrustedDomains         []string   `json:"trustedDomains"`
		MaintenanceWindowStart *time.Time `json:"maintenanceWindowStart"`
		MaintenanceWindowEnd   *time.Time `json:"maintenanceWindowEnd"`
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
	current.UpdatedAt = now
	_ = s.repository.UpsertPolicy(current)
	s.appendAudit(request, session.User, "policy.update", "policy", current.ID, "SUCCESS", map[string]any{"version": current.Version})
	writeJSON(writer, http.StatusOK, current)
}

func (s *Server) listBaselines(writer http.ResponseWriter, request *http.Request) {
	items := s.repository.ListBaselines(principal(request).User.TenantID, request.URL.Query().Get("assetId"))
	writeList(writer, items)
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
	writeList(writer, s.repository.ListJobs(principal(request).User.TenantID))
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

func (s *Server) listIncidents(writer http.ResponseWriter, request *http.Request) {
	incidents := s.repository.ListIncidents(principal(request).User.TenantID)
	s.updateIncidentMetrics(incidents)
	writeList(writer, incidents)
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

func (s *Server) transitionIncident(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	incident, err := s.repository.GetIncident(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
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
	s.appendAudit(request, session.User, "incident.transition", "incident", incident.ID, "SUCCESS", map[string]any{"status": input.Status})
	s.broker.Publish("incident.updated", incident)
	writeJSON(writer, http.StatusOK, incident)
}

func (s *Server) listEvidence(writer http.ResponseWriter, request *http.Request) {
	items := s.repository.ListEvidence(principal(request).User.TenantID, request.URL.Query().Get("assetId"))
	writeList(writer, items)
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

func (s *Server) listAlertChannels(writer http.ResponseWriter, request *http.Request) {
	items := s.repository.ListAlertChannels(principal(request).User.TenantID)
	for index := range items {
		items[index].URL = redactEndpoint(items[index].URL)
	}
	writeList(writer, items)
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
	writeList(writer, s.repository.ListAlertDeliveries(session.User.TenantID, incidentID))
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
	assets := s.repository.ListAssets(session.User.TenantID)
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
	jobs := s.repository.ListJobs(session.User.TenantID)
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
	incidents := s.repository.ListIncidents(session.User.TenantID)
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
	deliveries := s.repository.ListAlertDeliveries(session.User.TenantID, "")
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
func (s *Server) createAlertChannel(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	var input struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		URL     string `json:"url"`
		Secret  string `json:"secret"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	input.Type = strings.ToUpper(input.Type)
	if input.Type != "WEBHOOK" && input.Type != "WECHAT_WORK" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_CHANNEL", "渠道类型必须是 WEBHOOK 或 WECHAT_WORK", nil)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_CHANNEL", "渠道名称不能为空", nil)
		return
	}
	if _, _, err := s.guard.Validate(request.Context(), input.URL); err != nil {
		writeError(writer, request, http.StatusBadRequest, "UNSAFE_WEBHOOK", "通知地址不符合安全策略", nil)
		return
	}
	now := time.Now().UTC()
	channel := domain.AlertChannel{
		ID: identity.New(), TenantID: session.User.TenantID, Name: input.Name,
		Type: input.Type, URL: input.URL, Secret: input.Secret, Enabled: input.Enabled,
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

func (s *Server) listAudits(writer http.ResponseWriter, request *http.Request) {
	writeList(writer, s.repository.ListAudits(principal(request).User.TenantID))
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

func (s *Server) checkVerification(ctx context.Context, asset domain.Asset) error {
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
	token := strings.TrimSpace(string(result.RawHTML))
	hash := sha256.Sum256([]byte(token))
	expected := asset.VerificationTokenHash
	if expected == "" {
		expected = fmt.Sprintf("%x", sha256.Sum256([]byte(asset.VerificationToken)))
	}
	if result.Snapshot.StatusCode != http.StatusOK || subtle.ConstantTimeCompare([]byte(fmt.Sprintf("%x", hash)), []byte(expected)) != 1 {
		return errors.New("verification token mismatch")
	}
	return nil
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(sessionCookie)
		if err != nil {
			writeError(writer, request, http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录", nil)
			return
		}
		session, ok := s.auth.Get(cookie.Value)
		if !ok {
			writeError(writer, request, http.StatusUnauthorized, "SESSION_EXPIRED", "会话已失效", nil)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalKey{}, session)))
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
		for _, role := range roles {
			if current == role {
				handler(writer, request)
				return
			}
		}
		writeError(writer, request, http.StatusForbidden, "FORBIDDEN", "当前角色无权执行该操作", nil)
	}
}

func (s *Server) trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		captured := &metricsResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		traceID := request.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = identity.New()
		}
		writer.Header().Set("X-Request-ID", traceID)
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(captured, request.WithContext(context.WithValue(request.Context(), traceKey{}, traceID)))
		s.metrics.Inc("webmonitoring_http_requests_total", map[string]string{
			"method": request.Method, "status": strconv.Itoa(captured.status),
		})
		s.metrics.ObserveDuration("webmonitoring_http_request_duration_seconds", time.Since(started), map[string]string{
			"method": request.Method,
		})
	})
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

func principal(request *http.Request) auth.Session {
	session, _ := request.Context().Value(principalKey{}).(auth.Session)
	return session
}

func traceID(request *http.Request) string {
	value, _ := request.Context().Value(traceKey{}).(string)
	return value
}

func sessionResponse(session auth.Session) map[string]any {
	return map[string]any{
		"user": session.User, "csrfToken": session.CSRFToken, "expiresAt": session.ExpiresAt,
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

func writeList[T any](writer http.ResponseWriter, items []T) {
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "nextCursor": ""})
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
