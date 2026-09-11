package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

type Dispatcher struct {
	repository store.Repository
	guard      *security.URLGuard
	client     *http.Client
}

func NewDispatcher(repository store.Repository, guard *security.URLGuard) *Dispatcher {
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: guard.DialContext,
	}
	return &Dispatcher{
		repository: repository,
		guard:      guard,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("notification redirect limit exceeded")
				}
				_, _, err := guard.Validate(request.Context(), request.URL.String())
				return err
			},
		},
	}
}

func (d *Dispatcher) SendIncident(ctx context.Context, incident domain.Incident, asset domain.Asset) []error {
	channels := d.repository.ListAlertChannelsWithSecrets(incident.TenantID)
	failures := make([]error, 0)
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		if err := d.sendWithRetry(ctx, channel, incident, asset); err != nil {
			failures = append(failures, fmt.Errorf("channel %s: %w", channel.ID, err))
		}
	}
	return failures
}

func (d *Dispatcher) sendWithRetry(
	ctx context.Context,
	channel domain.AlertChannel,
	incident domain.Incident,
	asset domain.Asset,
) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(1<<attempt) * 250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := d.send(ctx, channel, incident, asset); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (d *Dispatcher) send(
	ctx context.Context,
	channel domain.AlertChannel,
	incident domain.Incident,
	asset domain.Asset,
) error {
	if _, _, err := d.guard.Validate(ctx, channel.URL); err != nil {
		return fmt.Errorf("unsafe notification target: %w", err)
	}
	var payload any
	if channel.Type == "WECHAT_WORK" {
		payload = map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": fmt.Sprintf(
					"**网页风险告警**\n>资产：%s\n>风险：%s\n>事件：%s",
					asset.Name,
					incident.Severity,
					incident.Title,
				),
			},
		}
	} else {
		payload = map[string]any{
			"event": "incident.created",
			"data": map[string]any{
				"incidentId": incident.ID,
				"assetId":    asset.ID,
				"assetName":  asset.Name,
				"severity":   incident.Severity,
				"title":      incident.Title,
				"createdAt":  incident.CreatedAt,
			},
		}
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "WebMonitoring-Notifier/0.1")
	if channel.Type == "WEBHOOK" && channel.Secret != "" {
		mac := hmac.New(sha256.New, []byte(channel.Secret))
		_, _ = mac.Write(body)
		request.Header.Set("X-WebMonitoring-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %d", response.StatusCode)
	}
	return nil
}
