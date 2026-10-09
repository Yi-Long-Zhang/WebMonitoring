package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/auth"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/store"
)

// reportRequestFailure is the HTTP answer a report builder wants after a request
// it could not satisfy.
//
// The builders are shared between the JSON and the HTML rendering, so they
// cannot write the response themselves without one form inheriting the other's
// content type. Returning a value keeps the choice of rendering with the
// handler, which is the only place that knows which one was asked for.
type reportRequestFailure struct {
	status    int
	code      string
	message   string
	details   map[string]any
	operation string
	err       error
}

func notFoundFailure() *reportRequestFailure {
	return &reportRequestFailure{status: http.StatusNotFound, code: "NOT_FOUND", message: "资源不存在"}
}

// repositoryFailure marks a storage error, which is reported through the same
// helper the rest of the API uses so the log line and the response are
// identical whichever rendering asked for the data.
func repositoryFailure(operation string, err error) *reportRequestFailure {
	return &reportRequestFailure{operation: operation, err: err}
}

func (f *reportRequestFailure) write(s *Server, writer http.ResponseWriter, request *http.Request) {
	if f.err != nil {
		s.repositoryFailure(writer, request, f.operation, f.err)
		return
	}
	writeError(writer, request, f.status, f.code, f.message, f.details)
}

// reportDocument is what the print-ready report template renders. It is a plain
// view model: everything in it comes from the repository, and nothing in it is
// trusted -- the template escapes every field, which matters because a signal
// summary, an incident title and an operator's note are all attacker-influenced
// or human-entered text.
type reportDocument struct {
	Title       string
	Subtitle    string
	GeneratedAt string
	Tenant      string
	// Sections is ordered: the template renders them in sequence, so the report
	// reads the same way every time rather than following map iteration order.
	Sections []reportSection
	// Notice is the closing statement -- provenance for an incident report, the
	// evidence rule for a summary.
	Notice string
}

type reportSection struct {
	Heading string
	// Fields is rendered as a definition list; Rows as a table.
	Fields []reportField
	Rows   []reportRow
	// Columns names the table columns, in order.
	Columns []string
	// Caption documents the section's scope or its exclusions.
	Caption string
}

type reportField struct {
	Label string
	Value string
}

// reportRow is one table line. Cells are pre-formatted strings so the view model
// stays free of template logic; the template escapes them on the way out.
type reportRow []string

const reportPrintStyles = `
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; padding: 32px 36px; color: #1f2933; background: #ffffff;
  font: 14px/1.6 "Helvetica Neue", Arial, "PingFang SC", "Microsoft YaHei", sans-serif; }
header { border-bottom: 2px solid #1f2933; padding-bottom: 14px; margin-bottom: 22px; }
h1 { margin: 0 0 6px; font-size: 22px; }
h2 { margin: 26px 0 10px; font-size: 16px; border-left: 3px solid #2b6cb0; padding-left: 9px; }
.subtitle { color: #52606d; font-size: 13px; }
.meta { margin-top: 8px; color: #52606d; font-size: 12px; }
dl { display: grid; grid-template-columns: 150px 1fr; gap: 6px 14px; margin: 0 0 6px; }
dt { color: #52606d; }
dd { margin: 0; word-break: break-word; }
table { width: 100%; border-collapse: collapse; margin-top: 6px; font-size: 13px; }
th, td { border: 1px solid #d9e2ec; padding: 6px 9px; text-align: left; vertical-align: top; word-break: break-word; }
th { background: #f0f4f8; font-weight: 600; }
.caption { margin-top: 6px; color: #52606d; font-size: 12px; }
.empty { color: #52606d; font-style: italic; }
.notice { margin-top: 30px; padding-top: 12px; border-top: 1px solid #d9e2ec; color: #52606d; font-size: 12px; }
@media print {
  body { padding: 0; font-size: 11pt; }
  h2 { break-after: avoid; }
  tr, dl { break-inside: avoid; }
}
`

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ .Title }}</title>
<style nonce="{{ .Nonce }}">{{ .Styles }}</style>
</head>
<body>
<header>
  <h1>{{ .Document.Title }}</h1>
  {{ if .Document.Subtitle }}<p class="subtitle">{{ .Document.Subtitle }}</p>{{ end }}
  <p class="meta">生成时间：{{ .Document.GeneratedAt }}{{ if .Document.Tenant }} ｜ 租户：{{ .Document.Tenant }}{{ end }}</p>
</header>
{{ range .Document.Sections }}
<section>
  <h2>{{ .Heading }}</h2>
  {{ if .Fields }}<dl>{{ range .Fields }}<dt>{{ .Label }}</dt><dd>{{ .Value }}</dd>{{ end }}</dl>{{ end }}
  {{ if .Columns }}
    {{ if .Rows }}
      <table>
        <thead><tr>{{ range .Columns }}<th>{{ . }}</th>{{ end }}</tr></thead>
        <tbody>{{ range .Rows }}<tr>{{ range . }}<td>{{ . }}</td>{{ end }}</tr>{{ end }}</tbody>
      </table>
    {{ else }}
      <p class="empty">无记录</p>
    {{ end }}
  {{ end }}
  {{ if .Caption }}<p class="caption">{{ .Caption }}</p>{{ end }}
</section>
{{ end }}
<p class="notice">{{ .Document.Notice }}</p>
</body>
</html>
`))

// writeReport renders the document as a self-contained page and sends it.
//
// The global Content-Security-Policy is default-src 'none', which includes
// style-src, so a plain <style> block would be dropped and the report would
// print unstyled. This handler therefore replaces the header for its own
// response with a policy that names a per-response nonce for the one style
// block it wrote. Scripts stay forbidden, which is what matters: the report
// renders attacker-influenced text, and an escaped page with no script is the
// defence.
func (s *Server) writeReport(writer http.ResponseWriter, request *http.Request, document reportDocument, filename string) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		s.logger.Error("report nonce generation failed", "error", err)
		writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法生成报告", nil)
		return
	}
	nonce := base64.RawStdEncoding.EncodeToString(nonceBytes)
	writer.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'nonce-"+nonce+"'; frame-ancestors 'none'")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	// inline, not attachment: the point of this rendering is that a person opens
	// it and prints it. The ZIP export remains the downloadable artifact.
	writer.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	var buffer bytes.Buffer
	if err := reportTemplate.Execute(&buffer, map[string]any{
		"Document": document, "Nonce": nonce,
		// template.CSS marks the value as trusted CSS so html/template leaves it
		// alone. Without it the contextual escaper rewrites the stylesheet --
		// measured: a 1.3 KB sheet came out as 8 bytes, and the report printed
		// with no styling at all while every test still passed.
		"Styles": template.CSS(reportPrintStyles), //nolint:gosec // 样式表是编译期常量、不含任何动态内容；用 template.CSS 是为了阻止 html/template 的 CSS 上下文重写它（被重写过一次，1.3KB→8 字节）
	}); err != nil {
		s.logger.Error("report render failed", "error", err)
		writeError(writer, request, http.StatusInternalServerError, "REPORT_FAILED", "无法生成报告", nil)
		return
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(buffer.Bytes())
}

// incidentReportHTML assembles the printable form of one incident.
//
// It is not a replacement for the ZIP export: that one carries the raw bytes of
// every piece of evidence and is what an auditor keeps. This one is what a
// person reads, and the notable difference is that it links to the evidence
// rather than embedding it -- a stored snapshot is the captured page itself, and
// inlining it would put attacker-controlled markup inside the report.
func (s *Server) incidentReportHTML(writer http.ResponseWriter, request *http.Request) {
	session := principal(request)
	incident, err := s.repository.GetIncident(request.Context(), session.User.TenantID, request.PathValue("incidentId"))
	if err != nil {
		s.notFound(writer, request)
		return
	}
	document, failure := s.buildIncidentReport(request.Context(), session, incident)
	if failure != nil {
		failure.write(s, writer, request)
		return
	}
	s.writeReport(writer, request, document, "incident-"+incident.ID+".html")
}

func (s *Server) buildIncidentReport(ctx context.Context, session auth.Session, incident domain.Incident) (reportDocument, *reportRequestFailure) {
	asset, err := s.repository.GetAsset(ctx, session.User.TenantID, incident.AssetID)
	if err != nil {
		return reportDocument{}, notFoundFailure()
	}
	events, _, err := s.repository.ListIncidentEvents(ctx, session.User.TenantID, incident.ID, store.ListOptions{})
	if err != nil {
		return reportDocument{}, repositoryFailure("list incident events for report", err)
	}
	evidenceRows := make([]reportRow, 0, len(incident.EvidenceIDs))
	for _, evidenceID := range incident.EvidenceIDs {
		item, getErr := s.repository.GetEvidence(ctx, session.User.TenantID, evidenceID)
		if getErr != nil {
			// Same stance as the ZIP export: a report that silently omits a piece
			// of evidence it was told about is worse than one that refuses.
			return reportDocument{}, &reportRequestFailure{
				status: http.StatusFailedDependency, code: "REPORT_INCOMPLETE",
				message: "报告所需证据元数据缺失", details: map[string]any{"evidenceId": evidenceID},
			}
		}
		evidenceRows = append(evidenceRows, reportRow{
			item.Kind, humanBytes(item.Size), formatReportTime(item.CreatedAt), item.SHA256, item.ID,
		})
	}

	owner := "未指派"
	if incident.OwnerID != "" {
		if user, userErr := s.repository.GetUser(ctx, session.User.TenantID, incident.OwnerID); userErr == nil {
			owner = user.Username
		} else {
			owner = incident.OwnerID
		}
	}
	signalRows := make([]reportRow, 0, len(incident.Signals))
	for _, signal := range incident.Signals {
		signalRows = append(signalRows, reportRow{
			signal.Kind, string(signal.Severity), signal.Summary, signal.RuleVersion, signal.Fingerprint,
		})
	}
	// The detail column carries whatever the entry actually holds: a transition
	// names both ends, a comment carries its text in Details, and a detection
	// entry carries the job it came from. Rendering only one of them would make
	// the timeline look emptier than the record is.
	timelineRows := make([]reportRow, 0, len(events))
	for _, event := range events {
		detail := ""
		switch {
		case event.FromStatus != "" && event.ToStatus != "":
			detail = string(event.FromStatus) + " -> " + string(event.ToStatus)
		}
		if note, ok := event.Details["note"].(string); ok && strings.TrimSpace(note) != "" {
			detail = strings.TrimSpace(detail + " " + note)
		}
		if event.JobID != "" && detail == "" {
			detail = "检测任务 " + event.JobID
		}
		actor := event.ActorID
		if actor == "" {
			actor = "系统"
		}
		timelineRows = append(timelineRows, reportRow{
			formatReportTime(event.CreatedAt), string(event.Type), actor, orDash(detail),
		})
	}

	document := reportDocument{
		Title:       "网页篡改监测事件报告",
		Subtitle:    incident.Title,
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04:05 MST"),
		Tenant:      session.User.TenantID,
		Sections: []reportSection{
			{
				Heading: "事件概况",
				Fields: []reportField{
					{Label: "事件 ID", Value: incident.ID},
					{Label: "风险等级", Value: string(incident.Severity)},
					{Label: "状态", Value: string(incident.Status)},
					{Label: "资产", Value: asset.Name},
					{Label: "网址", Value: asset.URL},
					{Label: "负责人", Value: owner},
					{Label: "触发次数", Value: strconv.Itoa(incident.OccurrenceCount)},
					{Label: "首次发现", Value: formatReportTime(incident.FirstSeenAt)},
					{Label: "最近发现", Value: formatReportTime(incident.LastSeenAt)},
					{Label: "基线版本", Value: fmt.Sprintf("v%d", incident.BaselineVersion)},
					{Label: "指纹", Value: incident.Fingerprint},
					{Label: "处置说明", Value: orDash(incident.Resolution)},
				},
			},
			{
				Heading: "检出信号",
				Columns: []string{"类型", "严重度", "摘要", "规则版本", "指纹"},
				Rows:    signalRows,
				Caption: "信号是本次检测判定该页面发生变化的依据，与基线版本一一对应。",
			},
			{
				Heading: "处置时间线",
				Columns: []string{"时间", "类型", "操作者", "说明"},
				Rows:    timelineRows,
				Caption: "事件行的 signals 会在每次复现时被覆盖，因此逐次明细只存在于时间线。",
			},
			{
				Heading: "证据清单",
				Columns: []string{"类型", "大小", "采集时间", "SHA-256", "证据 ID"},
				Rows:    evidenceRows,
				Caption: "报告只列出证据，不内嵌其内容：原始快照是不可信的网页内容，应通过证据接口单独下载核验。" +
					"完整原始证据请使用同一事件的 ZIP 导出（GET /api/v1/reports/{id}）。",
			},
		},
		Notice: "本报告由 WebMonitoring 自动生成。所有结论均关联证据、基线版本与规则版本；" +
			"报告本身不改变任何检测结论，也不替代原始证据。",
	}
	return document, nil
}

func (s *Server) summaryReportHTML(writer http.ResponseWriter, request *http.Request) {
	summary, failure := s.summarizeReport(request)
	if failure != nil {
		failure.write(s, writer, request)
		return
	}
	session := principal(request)
	s.writeReport(writer, request, buildSummaryReport(session, request, summary), "summary.html")
}

func buildSummaryReport(session auth.Session, request *http.Request, summary map[string]any) reportDocument {
	number := func(key string) int {
		switch value := summary[key].(type) {
		case int:
			return value
		case float64:
			return int(value)
		}
		return 0
	}
	assets := number("assets")
	verified := number("verifiedAssets")
	incidents := number("incidents")
	jobs := number("detectionJobs")
	succeeded := number("successfulJobs")
	failed := number("failedJobs")

	severityCounts, _ := summary["severityCounts"].(map[string]int)
	severityRows := make([]reportRow, 0, len(severityCounts))
	severities := make([]string, 0, len(severityCounts))
	for severity := range severityCounts {
		severities = append(severities, severity)
	}
	// Ordered most severe first rather than alphabetically: the point of the
	// table is to put the number that matters at the top.
	order := map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "INFO": 4}
	sort.Slice(severities, func(i, j int) bool { return order[severities[i]] < order[severities[j]] })
	for _, severity := range severities {
		severityRows = append(severityRows, reportRow{severity, strconv.Itoa(severityCounts[severity])})
	}

	// The keys are buildTrend's, spelled out here so a rename is a compile-time
	// thought rather than a table of <nil>: the first version of this renderer
	// guessed them and printed exactly that.
	trendRows := make([]reportRow, 0)
	if trend, ok := summary["trend"].([]map[string]any); ok {
		for _, day := range trend {
			trendRows = append(trendRows, reportRow{
				reportCell(day["date"]), reportCell(day["total"]),
				reportCell(day["critical"]), reportCell(day["high"]),
				reportCell(day["medium"]), reportCell(day["low"]), reportCell(day["info"]),
			})
		}
	}

	rangeText := "全部时间"
	if from := request.URL.Query().Get("from"); from != "" {
		rangeText = from
		if to := request.URL.Query().Get("to"); to != "" {
			rangeText += " 至 " + to
		} else {
			rangeText += " 起"
		}
	}
	return reportDocument{
		Title:       "网页篡改监测汇总报告",
		Subtitle:    "统计区间：" + rangeText,
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04:05 MST"),
		Tenant:      session.User.TenantID,
		Sections: []reportSection{
			{
				Heading: "总体情况",
				Fields: []reportField{
					{Label: "资产总数", Value: strconv.Itoa(assets)},
					{Label: "已验证资产", Value: strconv.Itoa(verified)},
					{Label: "检测任务", Value: strconv.Itoa(jobs)},
					{Label: "成功 / 失败", Value: fmt.Sprintf("%d / %d", succeeded, failed)},
					{Label: "安全事件", Value: strconv.Itoa(incidents)},
					{Label: "告警投递", Value: strconv.Itoa(number("alertDeliveries"))},
				},
			},
			{
				Heading: "响应时效",
				Fields: []reportField{
					{Label: "平均检测时延 (MTTD)", Value: formatSeconds(summary["mttdSeconds"])},
					{Label: "平均认领时延 (MTTA)", Value: formatSeconds(summary["mttaSeconds"])},
					{Label: "平均恢复时延 (MTTR)", Value: formatSeconds(summary["mttrSeconds"])},
				},
				Caption: "只统计区间内同时具备起点与终点的样本；少于一个样本显示为「-」，不代表零时延。",
			},
			{
				Heading: "事件风险分布",
				Columns: []string{"风险等级", "事件数"},
				Rows:    severityRows,
			},
			{
				Heading: "每日事件趋势",
				Columns: []string{"日期", "合计", "CRITICAL", "HIGH", "MEDIUM", "LOW", "INFO"},
				Rows:    trendRows,
				Caption: "区间内没有事件的日期以 0 列出，而不是省略：省略会让一段空白看起来像连续的活动。",
			},
		},
		Notice: "本汇总只做统计，不改变任何事件结论。逐条结论需回到事件报告与其关联证据。",
	}
}

// reportCell renders one table cell. It exists so an unexpected shape shows as
// an empty cell rather than as "<nil>" in a document someone hands to a customer.
func reportCell(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func formatReportTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format("2006-01-02 15:04:05 MST")
}

func formatSeconds(raw any) string {
	var seconds float64
	switch value := raw.(type) {
	case float64:
		seconds = value
	case int:
		seconds = float64(value)
	default:
		return "-"
	}
	if seconds <= 0 {
		return "-"
	}
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}

func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, exponent := float64(size), 0
	for value >= unit && exponent < 4 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGT"[exponent-1])
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
