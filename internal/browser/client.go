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
)

type Client struct {
	baseURL             string
	token               string
	client              *http.Client
	allowPrivateTargets bool
}

func NewClient(baseURL, token string, allowPrivateTargets bool) *Client {
	return &Client{
		baseURL:             strings.TrimRight(baseURL, "/"),
		token:               token,
		client:              &http.Client{Timeout: 90 * time.Second},
		allowPrivateTargets: allowPrivateTargets,
	}
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
	// Fail closed rather than snapshot the login form: login-state detection is
	// currently implemented on the HTTP checker path only.
	if policy.Auth != nil {
		return detection.CheckResult{}, errors.New("login-state detection is only supported in HTTP mode")
	}
	requestBody, _ := json.Marshal(map[string]any{
		"url":                 asset.URL,
		"timeoutSeconds":      policy.TimeoutSeconds,
		"maxResponseBytes":    policy.MaxResponseBytes,
		"ignoreSelectors":     policy.IgnoreSelectors,
		"allowPrivateTargets": c.allowPrivateTargets,
	})
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
