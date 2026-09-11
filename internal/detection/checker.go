package detection

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
)

const defaultMaxResponseBytes int64 = 5 * 1024 * 1024

type CheckResult struct {
	Snapshot   domain.Snapshot
	RawHTML    []byte
	Screenshot []byte
}

type Checker struct {
	guard     *security.URLGuard
	userAgent string
}

func NewChecker(guard *security.URLGuard) *Checker {
	return &Checker{guard: guard, userAgent: "WebMonitoring/0.1 (+authorized-integrity-monitor)"}
}

func (c *Checker) Check(ctx context.Context, asset domain.Asset, policy domain.MonitorPolicy) (CheckResult, error) {
	timeout := time.Duration(policy.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxBytes := policy.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	parsed, ips, err := c.guard.Validate(ctx, asset.URL)
	if err != nil {
		return CheckResult{}, err
	}
	redirects := make([]string, 0, 4)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           c.guard.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("redirect limit exceeded")
			}
			if _, _, validateErr := c.guard.Validate(request.Context(), request.URL.String()); validateErr != nil {
				return validateErr
			}
			redirects = append(redirects, request.URL.String())
			request.Header.Set("User-Agent", c.userAgent)
			return nil
		},
	}
	defer transport.CloseIdleConnections()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return CheckResult{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml")

	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return CheckResult{}, fmt.Errorf("request target: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return CheckResult{}, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return CheckResult{}, fmt.Errorf("response exceeds limit of %d bytes", maxBytes)
	}
	analysis, err := AnalyzeHTML(body, response.Request.URL.String(), policy.IgnoreSelectors)
	if err != nil {
		return CheckResult{}, err
	}
	resolvedIPs := make([]string, 0, len(ips))
	for _, ip := range ips {
		resolvedIPs = append(resolvedIPs, ip.String())
	}
	snapshot := domain.Snapshot{
		URL:             asset.URL,
		FinalURL:        response.Request.URL.String(),
		StatusCode:      response.StatusCode,
		Title:           analysis.Title,
		ContentHash:     hashString(analysis.CanonicalHTML),
		TextHash:        hashString(analysis.VisibleText),
		Scripts:         analysis.Scripts,
		Links:           analysis.Links,
		IFrames:         analysis.IFrames,
		Forms:           analysis.Forms,
		RedirectChain:   redirects,
		ResolvedIPs:     resolvedIPs,
		TLSFingerprint:  tlsFingerprint(response),
		ResponseHeaders: safeHeaders(response.Header),
		DurationMillis:  time.Since(started).Milliseconds(),
		CapturedAt:      time.Now().UTC(),
	}
	return CheckResult{Snapshot: snapshot, RawHTML: body}, nil
}

func tlsFingerprint(response *http.Response) string {
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(response.TLS.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

func safeHeaders(headers http.Header) map[string][]string {
	allowed := map[string]struct{}{
		"Cache-Control": {}, "Content-Length": {}, "Content-Security-Policy": {},
		"Content-Type": {}, "ETag": {}, "Last-Modified": {}, "Location": {},
		"Server": {}, "Strict-Transport-Security": {}, "X-Content-Type-Options": {},
		"X-Frame-Options": {},
	}
	result := make(map[string][]string)
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, ok := allowed[canonical]; ok {
			result[canonical] = append([]string(nil), values...)
		}
	}
	return result
}

func SameOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) &&
		strings.EqualFold(leftURL.Hostname(), rightURL.Hostname()) &&
		effectivePort(leftURL) == effectivePort(rightURL)
}

func effectivePort(value *url.URL) int {
	if value.Port() != "" {
		port, _ := strconv.Atoi(value.Port())
		return port
	}
	if value.Scheme == "https" {
		return 443
	}
	return 80
}
