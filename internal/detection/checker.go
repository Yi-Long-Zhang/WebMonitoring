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
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"

	otelattr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const defaultMaxResponseBytes int64 = 5 * 1024 * 1024

type CheckResult struct {
	Snapshot   domain.Snapshot
	RawHTML    []byte
	Screenshot []byte
}

type Checker struct {
	guard     *security.URLGuard
	cipher    security.SecretBox
	userAgent string
}

func NewChecker(guard *security.URLGuard) *Checker {
	return &Checker{guard: guard, userAgent: "WebMonitoring/0.1 (+authorized-integrity-monitor)"}
}

// SetCipher enables login-state detection. It is optional for plain HTTP checks
// but required whenever a policy carries an Auth config; Check fails closed when
// a login is needed and no cipher has been configured.
func (c *Checker) SetCipher(cipher security.SecretBox) {
	c.cipher = cipher
}

func (c *Checker) Check(ctx context.Context, asset domain.Asset, policy domain.MonitorPolicy) (CheckResult, error) {
	ctx, span := detectionTracer.Start(ctx, "checker.http",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			otelattr.String("webmonitoring.asset_id", asset.ID),
			otelattr.String("webmonitoring.policy_version", strconv.Itoa(policy.Version)),
		))
	defer span.End()
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
		span.RecordError(err)
		span.SetStatus(codes.Error, "unsafe target")
		return CheckResult{}, err
	}
	// The host is recorded but not logged: it is a customer URL and this span is
	// exported to an external collector.
	span.SetAttributes(otelattr.String("server.address", parsed.Hostname()))
	redirects := make([]string, 0, 4)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           c.guard.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	defer transport.CloseIdleConnections()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return CheckResult{}, fmt.Errorf("create cookie jar: %w", err)
	}
	validateRedirect := func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("redirect limit exceeded")
		}
		if _, _, validateErr := c.guard.Validate(request.Context(), request.URL.String()); validateErr != nil {
			return validateErr
		}
		request.Header.Set("User-Agent", c.userAgent)
		return nil
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		Jar:       jar,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if err := validateRedirect(request, via); err != nil {
				return err
			}
			redirects = append(redirects, request.URL.String())
			return nil
		},
	}

	// Login-state detection: authenticate first so the snapshot captures the
	// protected page, not the login form. The login POST is SSRF-guarded and
	// shares the cookie jar so the session is attached to the asset request.
	if policy.Auth != nil {
		loginClient := &http.Client{Transport: transport, Timeout: timeout, Jar: jar, CheckRedirect: validateRedirect}
		if err := c.authenticate(ctx, loginClient, policy.Auth, security.AuthAssociatedData(asset.TenantID, asset.ID), maxBytes); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "login failed")
			return CheckResult{}, err
		}
	}

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
	defer response.Body.Close() //nolint:errcheck // 延迟清理：此处失败已无可补救，调用方也不依赖它
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
		URL:              asset.URL,
		FinalURL:         response.Request.URL.String(),
		StatusCode:       response.StatusCode,
		Title:            analysis.Title,
		ContentHash:      hashString(analysis.CanonicalHTML),
		TextHash:         hashString(analysis.VisibleText),
		Scripts:          analysis.Scripts,
		Links:            analysis.Links,
		IFrames:          analysis.IFrames,
		Forms:            analysis.Forms,
		RedirectChain:    redirects,
		ResolvedIPs:      resolvedIPs,
		TLSFingerprint:   tlsFingerprint(response),
		ResponseHeaders:  safeHeaders(response.Header),
		ResourceRequests: analysis.Resources,
		DurationMillis:   time.Since(started).Milliseconds(),
		CapturedAt:       time.Now().UTC(),
	}
	return CheckResult{Snapshot: snapshot, RawHTML: body}, nil
}

func (c *Checker) authenticate(ctx context.Context, client *http.Client, auth *domain.AuthConfig, associatedData string, maxBytes int64) error {
	if c.cipher == nil {
		return errors.New("login-state detection requires a secret cipher")
	}
	username, err := c.cipher.Decrypt(auth.EncryptedUsername, associatedData)
	if err != nil {
		return fmt.Errorf("decrypt login username: %w", err)
	}
	password, err := c.cipher.Decrypt(auth.EncryptedPassword, associatedData)
	if err != nil {
		return fmt.Errorf("decrypt login password: %w", err)
	}
	loginURL, _, err := c.guard.Validate(ctx, auth.LoginURL)
	if err != nil {
		return fmt.Errorf("unsafe login URL: %w", err)
	}
	form := url.Values{}
	form.Set(auth.UsernameField, username)
	form.Set(auth.PasswordField, password)
	for key, value := range auth.ExtraFields {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", c.userAgent)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer response.Body.Close()                                           //nolint:errcheck // 延迟清理：此处失败已无可补救，调用方也不依赖它
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBytes+1)) //nolint:errcheck // 排空响应体或测试写入：目标不会失败，结果也不参与判定
	if response.StatusCode >= 500 {
		return fmt.Errorf("login endpoint returned status %d", response.StatusCode)
	}
	return nil
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
		port, _ := strconv.Atoi(value.Port()) //nolint:errcheck // 端口来自已解析的 URL，解析失败会回落到 0，随后被 URL 校验拒绝
		return port
	}
	if value.Scheme == "https" {
		return 443
	}
	return 80
}
