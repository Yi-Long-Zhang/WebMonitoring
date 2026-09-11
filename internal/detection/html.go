package detection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"webmonitoring/internal/domain"
)

type HTMLAnalysis struct {
	CanonicalHTML string
	VisibleText   string
	Title         string
	Scripts       []domain.ResourceRef
	Links         []string
	IFrames       []string
	Forms         []domain.FormRef
}

func AnalyzeHTML(raw []byte, baseURL string, ignoreSelectors []string) (HTMLAnalysis, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return HTMLAnalysis{}, fmt.Errorf("parse HTML: %w", err)
	}
	matchers := make([]cascadia.Matcher, 0, len(ignoreSelectors))
	for _, selector := range ignoreSelectors {
		matcher, parseErr := cascadia.Parse(selector)
		if parseErr != nil {
			return HTMLAnalysis{}, fmt.Errorf("parse ignore selector %q: %w", selector, parseErr)
		}
		matchers = append(matchers, matcher)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return HTMLAnalysis{}, fmt.Errorf("parse base URL: %w", err)
	}

	analysis := HTMLAnalysis{}
	var canonical strings.Builder
	var visible strings.Builder
	walkHTML(root, false, matchers, base, &canonical, &visible, &analysis)
	analysis.CanonicalHTML = strings.TrimSpace(canonical.String())
	analysis.VisibleText = normalizeWhitespace(visible.String())
	analysis.Scripts = uniqueResources(analysis.Scripts)
	analysis.Links = uniqueStrings(analysis.Links)
	analysis.IFrames = uniqueStrings(analysis.IFrames)
	sort.Slice(analysis.Forms, func(i, j int) bool {
		if analysis.Forms[i].Action == analysis.Forms[j].Action {
			return analysis.Forms[i].Method < analysis.Forms[j].Method
		}
		return analysis.Forms[i].Action < analysis.Forms[j].Action
	})
	return analysis, nil
}

func walkHTML(
	node *html.Node,
	hidden bool,
	matchers []cascadia.Matcher,
	base *url.URL,
	canonical, visible *strings.Builder,
	analysis *HTMLAnalysis,
) {
	if node.Type == html.ElementNode && matchesAny(node, matchers) {
		return
	}
	currentHidden := hidden
	if node.Type == html.ElementNode {
		tag := strings.ToLower(node.Data)
		currentHidden = hidden || tag == "script" || tag == "style" || tag == "title" || tag == "noscript" ||
			tag == "template" || hasHiddenAttribute(node)
		writeOpenTag(canonical, node)
		switch tag {
		case "title":
			analysis.Title = normalizeWhitespace(nodeText(node))
		case "script":
			source := resolveReference(base, attribute(node, "src"))
			if source != "" {
				analysis.Scripts = append(analysis.Scripts, domain.ResourceRef{URL: source})
			} else {
				inline := normalizeWhitespace(nodeText(node))
				if inline != "" {
					analysis.Scripts = append(analysis.Scripts, domain.ResourceRef{InlineHash: hashString(inline)})
				}
			}
		case "a":
			if target := resolveReference(base, attribute(node, "href")); target != "" {
				analysis.Links = append(analysis.Links, target)
			}
		case "iframe":
			if target := resolveReference(base, attribute(node, "src")); target != "" {
				analysis.IFrames = append(analysis.IFrames, target)
			}
		case "form":
			method := strings.ToUpper(attribute(node, "method"))
			if method == "" {
				method = "GET"
			}
			analysis.Forms = append(analysis.Forms, domain.FormRef{
				Action: resolveReference(base, attribute(node, "action")),
				Method: method,
			})
		}
	}
	if node.Type == html.TextNode {
		text := normalizeWhitespace(node.Data)
		if text != "" {
			canonical.WriteString(text)
			if !currentHidden {
				visible.WriteByte(' ')
				visible.WriteString(text)
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, currentHidden, matchers, base, canonical, visible, analysis)
	}
	if node.Type == html.ElementNode {
		canonical.WriteString("</")
		canonical.WriteString(strings.ToLower(node.Data))
		canonical.WriteByte('>')
	}
}

func writeOpenTag(builder *strings.Builder, node *html.Node) {
	builder.WriteByte('<')
	builder.WriteString(strings.ToLower(node.Data))
	attributes := append([]html.Attribute(nil), node.Attr...)
	sort.Slice(attributes, func(i, j int) bool {
		left := attributes[i].Namespace + ":" + attributes[i].Key
		right := attributes[j].Namespace + ":" + attributes[j].Key
		return left < right
	})
	for _, attr := range attributes {
		key := strings.ToLower(attr.Key)
		if key == "nonce" {
			continue
		}
		builder.WriteByte(' ')
		builder.WriteString(key)
		builder.WriteString(`="`)
		builder.WriteString(html.EscapeString(normalizeWhitespace(attr.Val)))
		builder.WriteByte('"')
	}
	builder.WriteByte('>')
}

func matchesAny(node *html.Node, matchers []cascadia.Matcher) bool {
	for _, matcher := range matchers {
		if matcher.Match(node) {
			return true
		}
	}
	return false
}

func hasHiddenAttribute(node *html.Node) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, "hidden") {
			return true
		}
	}
	return false
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteByte(' ')
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return builder.String()
}

func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

func resolveReference(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(strings.ToLower(raw), "javascript:") ||
		strings.HasPrefix(strings.ToLower(raw), "data:") || strings.HasPrefix(raw, "#") {
		return ""
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(reference)
	resolved.Fragment = ""
	return resolved.String()
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueResources(values []domain.ResourceRef) []domain.ResourceRef {
	seen := make(map[string]struct{}, len(values))
	result := make([]domain.ResourceRef, 0, len(values))
	for _, value := range values {
		key := value.URL + ":" + value.InlineHash
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].URL+result[i].InlineHash < result[j].URL+result[j].InlineHash
	})
	return result
}
