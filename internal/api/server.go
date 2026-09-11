package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	}
	server.handler = server.routes()
	return server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) routes() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.health)
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
	protected.HandleFunc("GET /api/v1/detection-jobs", s.listJobs)
	protected.HandleFunc("POST /api/v1/detection-jobs", s.requireRoles(s.createJob, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/incidents", s.listIncidents)
	protected.HandleFunc("GET /api/v1/incidents/{id}", s.getIncident)
	protected.HandleFunc("POST /api/v1/incidents/{id}/transition", s.requireRoles(s.transitionIncident, domain.RoleAdmin, domain.RoleOperator))
	protected.HandleFunc("GET /api/v1/evidence", s.listEvidence)
	protected.HandleFunc("GET /api/v1/evidence/{id}/download", s.downloadEvidence)
	protected.HandleFunc("GET /api/v1/alert-channels", s.listAlertChannels)
	protected.HandleFunc("POST /api/v1/alert-channels", s.requireRoles(s.createAlertChannel, domain.RoleAdmin))
	protected.HandleFunc("GET /api/v1/audit-events", s.listAudits)
	protected.HandleFunc("GET /api/v1/reports/{incidentId}", s.downloadReport)
	protected.HandleFunc("GET /api/v1/events/stream", s.streamEvents)

	root.Handle("/api/v1/", s.authenticate(s.csrf(protected)))
	return s.trace(root)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
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
	}
	if err := s.repository.CreateAsset(asset); err != nil {
		writeError(writer, request, http.StatusConflict, "ASSET_EXISTS", "该 URL 已经登记", nil)
		return
	}
	policy := defaultPolicy(asset)
	_ = s.repository.UpsertPolicy(policy)
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
	_ = s.repository.UpdateAsset(asset)
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
		asset.VerificationToken = "webmonitoring-verification=" + identity.New()
		asset.VerificationIssuedAt = &now
		asset.VerificationStatus = domain.VerificationPending
		asset.UpdatedAt = now
		_ = s.repository.UpdateAsset(asset)
		s.appendAudit(request, session.User, "asset.verification.issue", "asset", asset.ID, "SUCCESS", nil)
		writeJSON(writer, http.StatusOK, map[string]string{
			"path": "/.well-known/webmonitoring-verification.txt", "content": asset.VerificationToken,
		})
		return
	}
	if strings.ToUpper(input.Action) != "VERIFY" || asset.VerificationToken == "" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_ACTION", "请先签发验证令牌", nil)
		return
	}
	if err := s.checkVerification(request.Context(), asset); err != nil {
		s.appendAudit(request, session.User, "asset.verification.verify", "asset", asset.ID, "FAILED", nil)
		writeError(writer, request, http.StatusUnprocessableEntity, "VERIFICATION_FAILED", "未在目标站点找到有效令牌", nil)
		return
	}
	asset.VerificationStatus = domain.VerificationVerified
	asset.Enabled = true
	asset.VerificationToken = ""
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

func (s *Server) changeBaselineStatus(writer http.ResponseWriter, request *http.Request, status domain.BaselineStatus) {
	session := principal(request)
	baseline, err := s.repository.GetBaseline(session.User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	if baseline.Status != domain.BaselineCandidate {
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
	writeList(writer, s.repository.ListIncidents(principal(request).User.TenantID))
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
	if (input.Status == domain.IncidentResolved || input.Status == domain.IncidentFalsePositive) &&
		strings.TrimSpace(input.Resolution) == "" {
		writeError(writer, request, http.StatusBadRequest, "RESOLUTION_REQUIRED", "解决或误报状态必须填写说明", nil)
		return
	}
	incident.Status = input.Status
	if input.OwnerID != "" {
		incident.OwnerID = input.OwnerID
	} else if input.Status == domain.IncidentAcknowledged && incident.OwnerID == "" {
		incident.OwnerID = session.User.ID
	}
	if input.Resolution != "" {
		incident.Resolution = strings.TrimSpace(input.Resolution)
	}
	if input.Status == domain.IncidentResolved || input.Status == domain.IncidentFalsePositive {
		now := time.Now().UTC()
		incident.ResolvedAt = &now
	}
	_ = s.repository.UpdateIncident(incident)
	s.appendAudit(request, session.User, "incident.transition", "incident", incident.ID, "SUCCESS", map[string]any{"status": input.Status})
	s.broker.Publish("incident.updated", incident)
	writeJSON(writer, http.StatusOK, incident)
}

func (s *Server) listEvidence(writer http.ResponseWriter, request *http.Request) {
	items := s.repository.ListEvidence(principal(request).User.TenantID, request.URL.Query().Get("assetId"))
	writeList(writer, items)
}

func (s *Server) downloadEvidence(writer http.ResponseWriter, request *http.Request) {
	item, err := s.repository.GetEvidence(principal(request).User.TenantID, request.PathValue("id"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	file, err := s.evidence.Open(item.ObjectKey)
	if err != nil {
		writeError(writer, request, http.StatusNotFound, "EVIDENCE_MISSING", "证据文件不存在", nil)
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", item.MediaType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+item.ID+safeExtension(item.Kind)+`"`)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(writer, file)
}

func (s *Server) listAlertChannels(writer http.ResponseWriter, request *http.Request) {
	items := s.repository.ListAlertChannels(principal(request).User.TenantID)
	for index := range items {
		items[index].URL = redactEndpoint(items[index].URL)
	}
	writeList(writer, items)
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
	summary, _ := archive.Create("incident.json")
	_ = json.NewEncoder(summary).Encode(incident)
	for _, evidenceID := range incident.EvidenceIDs {
		item, getErr := s.repository.GetEvidence(session.User.TenantID, evidenceID)
		if getErr != nil {
			continue
		}
		file, openErr := s.evidence.Open(item.ObjectKey)
		if openErr != nil {
			continue
		}
		entry, createErr := archive.Create("evidence/" + item.ID + safeExtension(item.Kind))
		if createErr == nil {
			_, _ = io.Copy(entry, file)
		}
		_ = file.Close()
	}
	_ = archive.Close()
	s.appendAudit(request, session.User, "report.export", "incident", incident.ID, "SUCCESS", nil)
	writer.Header().Set("Content-Type", "application/zip")
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
	if result.Snapshot.StatusCode != http.StatusOK ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(result.RawHTML))), []byte(asset.VerificationToken)) != 1 {
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
		traceID := request.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = identity.New()
		}
		writer.Header().Set("X-Request-ID", traceID)
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), traceKey{}, traceID)))
	})
}

func (s *Server) appendAudit(
	request *http.Request,
	user domain.User,
	action, resourceType, resourceID, result string,
	details map[string]any,
) {
	_ = s.repository.AppendAudit(domain.AuditEvent{
		ID: identity.New(), TenantID: user.TenantID, ActorID: user.ID, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Result: result,
		TraceID: traceID(request), Details: details, CreatedAt: time.Now().UTC(),
	})
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
		"code": code, "message": message, "traceId": traceID(request), "details": details,
	})
}

func parseLimit(request *http.Request) int {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}
