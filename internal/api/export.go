package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/store"
)

// exportFormat reads and validates the ?format= parameter, defaulting to JSON
// like the asset export does. An unknown value is refused rather than defaulted:
// a caller that asked for xlsx and silently got JSON would build on it.
func exportFormat(writer http.ResponseWriter, request *http.Request) (string, bool) {
	format := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(writer, request, http.StatusBadRequest, "INVALID_FORMAT", "导出格式必须是 json 或 csv", nil)
		return "", false
	}
	return format, true
}

// writeExport streams a CSV attachment. Like the asset export it is unbounded
// and bypasses the keyset page, because an export is an explicit request for
// everything the filter selects rather than a page of it.
func writeExport(writer http.ResponseWriter, request *http.Request, filename string, header []string, rows [][]string) {
	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
	csvWriter := csv.NewWriter(writer)
	_ = csvWriter.Write(header)
	for _, row := range rows {
		_ = csvWriter.Write(row)
	}
	csvWriter.Flush()
}

// exportIncidents answers the whole filtered incident list as one file.
//
// The filter is the same one the listing takes, and it goes through the same
// parser: an export that accepted a filter the listing would have rejected (or
// the reverse) would let a caller believe they had exported a subset they had
// not, which is worse in a file someone hands to a customer than on a screen
// they can scroll.
func (s *Server) exportIncidents(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	format, ok := exportFormat(writer, request)
	if !ok {
		return
	}
	filter, err := parseIncidentFilter(request)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_FILTER", err.Error(), nil)
		return
	}
	items, _, err := s.repository.ListIncidents(request.Context(), session.User.TenantID, filter, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "export incidents", err)
		return
	}
	s.appendAudit(request, session.User, "incident.export", "incident", "", "SUCCESS", map[string]any{
		"count": len(items), "format": format,
	})
	if format == "csv" {
		rows := make([][]string, 0, len(items))
		for _, incident := range items {
			rows = append(rows, []string{
				incident.ID, incident.AssetID, string(incident.Severity), string(incident.Status),
				incident.Title, incident.OwnerID, strconv.Itoa(incident.OccurrenceCount),
				incident.Fingerprint, strconv.Itoa(incident.BaselineVersion), incident.Resolution,
				incident.CreatedAt.UTC().Format(time.RFC3339),
				incident.FirstSeenAt.UTC().Format(time.RFC3339),
				incident.LastSeenAt.UTC().Format(time.RFC3339),
				formatOptionalTime(incident.AcknowledgedAt), formatOptionalTime(incident.ResolvedAt),
			})
		}
		writeExport(writer, request, "incidents.csv", []string{
			"id", "assetId", "severity", "status", "title", "ownerId", "occurrenceCount",
			"fingerprint", "baselineVersion", "resolution", "createdAt", "firstSeenAt",
			"lastSeenAt", "acknowledgedAt", "resolvedAt",
		}, rows)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// exportAudits answers the whole filtered audit trail as one file. It takes the
// same filter the listing does, for the same reason the incident export does.
func (s *Server) exportAudits(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	format, ok := exportFormat(writer, request)
	if !ok {
		return
	}
	filter, err := parseAuditFilter(request)
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, "INVALID_FILTER", err.Error(), nil)
		return
	}
	items, _, err := s.repository.ListAudits(request.Context(), session.User.TenantID, filter, store.ListOptions{})
	if err != nil {
		s.repositoryFailure(writer, request, "export audit events", err)
		return
	}
	s.appendAudit(request, session.User, "audit.export", "audit_event", "", "SUCCESS", map[string]any{
		"count": len(items), "format": format,
	})
	if format == "csv" {
		rows := make([][]string, 0, len(items))
		for _, event := range items {
			rows = append(rows, []string{
				event.ID, event.ActorID, event.Action, event.ResourceType, event.ResourceID,
				event.Result, event.TraceID, formatDetails(event.Details),
				event.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		writeExport(writer, request, "audit-events.csv", []string{
			"id", "actorId", "action", "resourceType", "resourceId", "result", "traceId", "details", "createdAt",
		}, rows)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// formatDetails renders an audit event's detail map as one CSV cell. It is
// JSON rather than a flattened column set because the shape differs per action,
// and inventing a union of every action's keys would produce a table that is
// mostly empty and still loses the nesting.
func formatDetails(details map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return ""
	}
	return string(encoded)
}
