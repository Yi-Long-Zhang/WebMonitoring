package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"webmonitoring/internal/detection"
	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
)

type Client struct {
	baseURL string
	token   string
	client  *http.Client
	guard   *security.URLGuard
	cipher  *security.SecretCipher
}

// NewClient takes the same URLGuard the caller uses for its own outbound checks,
// so the worker's SSRF policy and the login-URL policy cannot drift apart. A nil
// guard falls back to the most restrictive one rather than allowing anything.
func NewClient(baseURL, token string, guard *security.URLGuard) *Client {
	if guard == nil {
		guard = security.NewURLGuard(false)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 90 * time.Second},
		guard:   guard,
	}
}

// SetCipher enables login-state detection in browser mode. It is optional for
// plain checks but required whenever a policy carries an Auth config: without it
// Check fails closed rather than letting the worker render the login form and
// snapshot that as if it were the protected page.
func (c *Client) SetCipher(cipher *security.SecretCipher) {
	c.cipher = cipher
}

func (c *Client) Available() bool {
	return c.baseURL != "" && c.token != ""
}

func (c *Client) Ready(ctx context.Context) error {
	if !c.Available() {
		return errors.New("browser worker is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("browser worker returned %d", response.StatusCode)
	}
	return nil
}

func (c *Client) Check(ctx context.Context, asset domain.Asset, policy domain.MonitorPolicy) (detection.CheckResult, error) {
	if !c.Available() {
		return detection.CheckResult{}, errors.New("browser worker is not configured")
	}
	requestPayload := map[string]any{
		"url":                 asset.URL,
		"timeoutSeconds":      policy.TimeoutSeconds,
		"maxResponseBytes":    policy.MaxResponseBytes,
		"ignoreSelectors":     policy.IgnoreSelectors,
		"allowPrivateTargets": c.guard.AllowPrivate,
	}
	if policy.Auth != nil {
		login, err := c.loginPayload(ctx, asset, policy.Auth)
		if err != nil {
			return detection.CheckResult{}, err
		}
		requestPayload["auth"] = login
	}
	requestBody, _ := json.Marshal(requestPayload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/check", bytes.NewReader(requestBody))
	if err != nil {
		return detection.CheckResult{}, fmt.Errorf("create browser worker request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return detection.CheckResult{}, fmt.Errorf("call browser worker: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 15*1024*1024))
	if err != nil {
		return detection.CheckResult{}, fmt.Errorf("read browser worker response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return detection.CheckResult{}, fmt.Errorf("browser worker returned %d", response.StatusCode)
	}
	var payload struct {
		Snapshot         domain.Snapshot `json:"snapshot"`
		HTMLBase64       string          `json:"htmlBase64"`
		ScreenshotBase64 string          `json:"screenshotBase64"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return detection.CheckResult{}, fmt.Errorf("decode browser worker response: %w", err)
	}
	rawHTML, err := base64.StdEncoding.DecodeString(payload.HTMLBase64)
	if err != nil {
		return detection.CheckResult{}, fmt.Errorf("decode browser HTML: %w", err)
	}
	screenshot, err := base64.StdEncoding.DecodeString(payload.ScreenshotBase64)
	if err != nil {
		return detection.CheckResult{}, fmt.Errorf("decode browser screenshot: %w", err)
	}
	return detection.CheckResult{Snapshot: payload.Snapshot, RawHTML: rawHTML, Screenshot: screenshot}, nil
}

// loginPayload decrypts the stored credentials and revalidates the login URL
// before either leaves the control plane.
//
// This is the one place where plaintext customer credentials cross a process
// boundary. The ciphertext is bound to this tenant and asset through
// security.AuthAssociatedData, so a blob copied from another asset cannot be
// decrypted here, and the login URL is checked against the same SSRF guard the
// HTTP checker uses -- a policy written before this path existed, or by a
// process with a different ALLOW_PRIVATE_TARGETS, must not be able to point the
// worker at an internal address. ADR-0004 covers why the worker receives
// plaintext rather than the master key.
func (c *Client) loginPayload(ctx context.Context, asset domain.Asset, auth *domain.AuthConfig) (map[string]any, error) {
	if c.cipher == nil {
		return nil, errors.New("login-state detection requires a secret cipher")
	}
	associatedData := security.AuthAssociatedData(asset.TenantID, asset.ID)
	username, err := c.cipher.Decrypt(auth.EncryptedUsername, associatedData)
	if err != nil {
		return nil, fmt.Errorf("decrypt login username: %w", err)
	}
	password, err := c.cipher.Decrypt(auth.EncryptedPassword, associatedData)
	if err != nil {
		return nil, fmt.Errorf("decrypt login password: %w", err)
	}
	if _, _, err := c.guard.Validate(ctx, auth.LoginURL); err != nil {
		return nil, fmt.Errorf("unsafe login URL: %w", err)
	}
	return map[string]any{
		"loginUrl":       auth.LoginURL,
		"usernameField":  auth.UsernameField,
		"passwordField":  auth.PasswordField,
		"extraFields":    auth.ExtraFields,
		"submitSelector": auth.SubmitSelector,
		"username":       username,
		"password":       password,
	}, nil
}
