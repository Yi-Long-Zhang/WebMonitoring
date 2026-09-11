package detection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

const RuleVersion = "builtin-2026-09-11.1"

func Compare(before, after domain.Snapshot, trustedDomains []string, evidenceIDs []string) []domain.Signal {
	signals := make([]domain.Signal, 0)
	add := func(kind string, severity domain.Severity, summary string, oldValue, newValue any) {
		signals = append(signals, domain.Signal{
			ID:          identity.New(),
			Kind:        kind,
			Severity:    severity,
			Summary:     summary,
			Before:      oldValue,
			After:       newValue,
			EvidenceIDs: append([]string(nil), evidenceIDs...),
			RuleVersion: RuleVersion,
			Fingerprint: fingerprint(kind, oldValue, newValue),
		})
	}

	if before.StatusCode != after.StatusCode {
		severity := domain.SeverityMedium
		if after.StatusCode >= 500 || after.StatusCode == 0 {
			severity = domain.SeverityHigh
		}
		add("HTTP_STATUS_CHANGED", severity, "HTTP 状态码发生变化", before.StatusCode, after.StatusCode)
	}
	if before.Title != after.Title {
		add("TITLE_CHANGED", domain.SeverityMedium, "页面标题发生变化", before.Title, after.Title)
	}
	if before.TextHash != after.TextHash {
		add("VISIBLE_TEXT_CHANGED", domain.SeverityLow, "页面可见正文发生变化", before.TextHash, after.TextHash)
	}
	if before.ContentHash != after.ContentHash {
		add("DOM_CHANGED", domain.SeverityLow, "规范化 DOM 发生变化", before.ContentHash, after.ContentHash)
	}
	if !SameOrigin(after.URL, after.FinalURL) {
		add("CROSS_ORIGIN_REDIRECT", domain.SeverityHigh, "页面跳转到不同来源", before.FinalURL, after.FinalURL)
	}
	newScripts := addedResources(before.Scripts, after.Scripts)
	for _, script := range newScripts {
		severity := domain.SeverityMedium
		if script.URL != "" && !isTrustedURL(script.URL, after.URL, trustedDomains) {
			severity = domain.SeverityHigh
		}
		add("SCRIPT_ADDED", severity, "页面新增脚本", nil, script)
	}
	for _, frame := range addedStrings(before.IFrames, after.IFrames) {
		severity := domain.SeverityMedium
		if !isTrustedURL(frame, after.URL, trustedDomains) {
			severity = domain.SeverityHigh
		}
		add("IFRAME_ADDED", severity, "页面新增 iframe", nil, frame)
	}
	for _, link := range addedStrings(before.Links, after.Links) {
		if !isTrustedURL(link, after.URL, trustedDomains) {
			add("UNTRUSTED_LINK_ADDED", domain.SeverityMedium, "页面新增非可信外链", nil, link)
		}
	}
	if !formsEqual(before.Forms, after.Forms) {
		add("FORM_CHANGED", domain.SeverityHigh, "页面表单提交目标或方法发生变化", before.Forms, after.Forms)
	}
	return signals
}

func OverallSeverity(signals []domain.Signal) domain.Severity {
	highCount := 0
	result := domain.SeverityInfo
	for _, signal := range signals {
		if signal.Severity == domain.SeverityHigh || signal.Severity == domain.SeverityCritical {
			highCount++
		}
		if severityRank(signal.Severity) > severityRank(result) {
			result = signal.Severity
		}
	}
	if highCount >= 2 {
		return domain.SeverityCritical
	}
	return result
}

func IncidentFingerprint(signals []domain.Signal) string {
	parts := make([]string, 0, len(signals))
	for _, signal := range signals {
		parts = append(parts, signal.Fingerprint)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func fingerprint(kind string, before, after any) string {
	payload, _ := json.Marshal([]any{kind, before, after})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func addedStrings(before, after []string) []string {
	existing := make(map[string]struct{}, len(before))
	for _, value := range before {
		existing[value] = struct{}{}
	}
	result := make([]string, 0)
	for _, value := range after {
		if _, ok := existing[value]; !ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func addedResources(before, after []domain.ResourceRef) []domain.ResourceRef {
	existing := make(map[string]struct{}, len(before))
	for _, value := range before {
		existing[value.URL+":"+value.InlineHash] = struct{}{}
	}
	result := make([]domain.ResourceRef, 0)
	for _, value := range after {
		if _, ok := existing[value.URL+":"+value.InlineHash]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func formsEqual(left, right []domain.FormRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func isTrustedURL(rawURL, assetURL string, trustedDomains []string) bool {
	target, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	asset, err := url.Parse(assetURL)
	if err == nil && strings.EqualFold(target.Hostname(), asset.Hostname()) {
		return true
	}
	for _, trusted := range trustedDomains {
		trusted = strings.ToLower(strings.TrimSpace(trusted))
		host := strings.ToLower(target.Hostname())
		if host == trusted || strings.HasSuffix(host, "."+trusted) {
			return true
		}
	}
	return false
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

func ValidateTransition(from, to domain.IncidentStatus) error {
	allowed := map[domain.IncidentStatus]map[domain.IncidentStatus]struct{}{
		domain.IncidentOpen: {
			domain.IncidentAcknowledged: {}, domain.IncidentFalsePositive: {},
		},
		domain.IncidentAcknowledged: {
			domain.IncidentInvestigating: {},
		},
		domain.IncidentInvestigating: {
			domain.IncidentResolved: {}, domain.IncidentFalsePositive: {},
		},
		domain.IncidentResolved: {
			domain.IncidentClosed: {}, domain.IncidentInvestigating: {},
		},
	}
	if _, ok := allowed[from][to]; !ok {
		return fmt.Errorf("incident transition %s -> %s is not allowed", from, to)
	}
	return nil
}
