package api

import (
	"context"
	"net/http"
	"os"
	"time"

	"webmonitoring/internal/config"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// escalationBatch bounds one sweep. A pass that has fallen behind catches up
// over several ticks rather than pulling every unclaimed incident into memory at
// once; the oldest are reported first, so falling behind delays the newest
// escalation instead of dropping it.
const escalationBatch = 100

// escalationMaintenance is the authorized manual trigger, mirroring the manual
// retention run: it is the same sweep the timer drives, so an operator can see
// what it would report without waiting for the next tick. The response is the
// count, because "it ran" and "it found something" are different answers.
func (s *Server) escalationMaintenance(writer http.ResponseWriter, request *http.Request) {
	unclaimedFor := time.Duration(config.PositiveInt(os.Getenv, "ESCALATION_UNCLAIMED_MINUTES", 30)) * time.Minute
	before := time.Now().UTC()
	reported := s.runEscalation(request.Context(), unclaimedFor)
	s.appendAudit(request, principal(request).User, "maintenance.escalation", "incident", "", "SUCCESS",
		map[string]any{"reported": reported, "unclaimedMinutes": int(unclaimedFor.Minutes())})
	writeJSON(writer, http.StatusOK, map[string]any{
		"reported": reported, "unclaimedMinutes": int(unclaimedFor.Minutes()),
		"ranAt": before.Format(time.RFC3339),
	})
}

// RunEscalation reports high-risk incidents nobody has claimed within the
// threshold.
//
// This is the second half of the same problem the failure-streak notice solves:
// a high-risk incident that sits unowned produces no signal at all, so an
// installation with nobody watching its queue looks exactly like one with
// nothing to do. The notice goes to the tenant's administrators' addresses,
// because the point is to reach someone who can assign it.
//
// The stamp is claimed before anything is sent, and only the caller that wins it
// notifies. Two replicas sweep on their own timers, and the alternative --
// notifying first and stamping afterwards -- would send the same escalation
// twice whenever they overlap.
func (s *Server) RunEscalation(ctx context.Context, unclaimedFor time.Duration) {
	s.runEscalation(ctx, unclaimedFor)
}

// runEscalation is the body, shared by the timer and the manual trigger. It
// returns how many incidents it reported.
func (s *Server) runEscalation(ctx context.Context, unclaimedFor time.Duration) int {
	cutoff := time.Now().UTC().Add(-unclaimedFor)
	candidates, err := s.repository.ListUnownedHighRiskIncidents(ctx, cutoff, escalationBatch)
	if err != nil {
		s.logger.Warn("escalation sweep could not read unclaimed incidents", "error", err)
		return 0
	}
	reported := 0
	for _, incident := range candidates {
		if ctx.Err() != nil {
			return reported
		}
		if s.escalateIncident(ctx, incident, unclaimedFor) {
			reported++
		}
	}
	return reported
}

// escalateIncident reports one incident. It returns whether the caller won the
// stamp, which is what the sweep counts as reported.
func (s *Server) escalateIncident(ctx context.Context, incident domain.Incident, unclaimedFor time.Duration) bool {
	asset, err := s.repository.GetAsset(ctx, incident.TenantID, incident.AssetID)
	if err != nil {
		s.logger.Warn("escalation could not load the incident's asset",
			"incident_id", incident.ID, "error", err)
		return false
	}
	users, err := s.repository.ListUsers(ctx, incident.TenantID)
	if err != nil {
		s.logger.Warn("escalation could not list the tenant's administrators",
			"incident_id", incident.ID, "error", err)
		return false
	}
	// Claimed before sending: losing the race must mean staying quiet, not
	// sending a duplicate.
	stamped, err := s.repository.MarkIncidentEscalated(ctx, incident.TenantID, incident.ID, time.Now().UTC())
	if err != nil {
		s.logger.Warn("escalation could not stamp the incident", "incident_id", incident.ID, "error", err)
		return false
	}
	if !stamped {
		return false
	}
	notified := 0
	for _, user := range users {
		if user.Role != domain.RoleAdmin || user.Disabled || user.Email == "" {
			continue
		}
		s.detection.NotifyEscalation(ctx, user, incident, asset, unclaimedFor)
		notified++
	}
	// The audit row is written whether or not an address existed, because "the
	// platform noticed and could not tell anyone" is itself something an
	// operator needs to be able to find out afterwards.
	details := map[string]any{"severity": string(incident.Severity), "adminsNotified": notified}
	if err := s.repository.AppendAudit(ctx, domain.AuditEvent{
		ID: identity.New(), TenantID: incident.TenantID, Action: "incident.escalated",
		ResourceType: "incident", ResourceID: incident.ID, Result: "SUCCESS",
		TraceID: "escalation", Details: details, CreatedAt: time.Now().UTC(),
	}); err != nil {
		s.logger.Warn("escalation could not record its audit entry", "incident_id", incident.ID, "error", err)
	}
	s.logger.Warn("high-risk incident escalated with no owner",
		"incident_id", incident.ID, "severity", incident.Severity, "admins_notified", notified)
	return true
}
