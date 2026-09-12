package security

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var (
	jsonSecretPattern = regexp.MustCompile(`(?i)("(?:authorization|proxy-authorization|cookie|set-cookie|password|passwd|token|secret|api[_-]?key|access[_-]?key)"\s*:\s*)"[^"]*"`)
	pairSecretPattern = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|password|passwd|token|secret|api[_-]?key|access[_-]?key)\s*[:=]\s*[^\s,;]+`)
	authPattern       = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	urlUserPattern    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)
)

func RedactString(value string) string {
	value = jsonSecretPattern.ReplaceAllString(value, `${1}"`+redacted+`"`)
	value = authPattern.ReplaceAllString(value, `${1} `+redacted)
	value = pairSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		if separator < 0 {
			return redacted
		}
		return match[:separator+1] + redacted
	})
	return urlUserPattern.ReplaceAllString(value, `${1}`+redacted+`@`)
}

func IsSensitiveName(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(name))
	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie",
		"password", "passwd", "token", "secret", "apikey", "accesskey":
		return true
	default:
		return false
	}
}

func RedactAny(value any) any {
	switch current := value.(type) {
	case string:
		return RedactString(current)
	case error:
		return RedactString(current.Error())
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			if IsSensitiveName(key) {
				result[key] = redacted
			} else {
				result[key] = RedactAny(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			result[index] = RedactAny(item)
		}
		return result
	default:
		return value
	}
}

type RedactingHandler struct {
	next slog.Handler
}

func NewRedactingHandler(next slog.Handler) slog.Handler {
	return &RedactingHandler{next: next}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, RedactString(record.Message), record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		clean.AddAttrs(redactAttr(attribute))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *RedactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attributes))
	for index, attribute := range attributes {
		clean[index] = redactAttr(attribute)
	}
	return &RedactingHandler{next: h.next.WithAttrs(clean)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if IsSensitiveName(attribute.Key) {
		return slog.String(attribute.Key, redacted)
	}
	switch attribute.Value.Kind() {
	case slog.KindString:
		attribute.Value = slog.StringValue(RedactString(attribute.Value.String()))
	case slog.KindAny:
		attribute.Value = slog.StringValue(fmt.Sprint(RedactAny(attribute.Value.Any())))
	case slog.KindGroup:
		group := attribute.Value.Group()
		for index := range group {
			group[index] = redactAttr(group[index])
		}
		attribute.Value = slog.GroupValue(group...)
	}
	return attribute
}
