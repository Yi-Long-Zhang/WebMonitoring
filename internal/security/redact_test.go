package security

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactStringRemovesCommonCredentials(t *testing.T) {
	input := `password=hunter2 Authorization: Bearer abc.def token=tok123 Cookie: session=secret https://user:pass@example.com "secret":"notify-key"`
	output := RedactString(input)
	for _, secret := range []string{"hunter2", "abc.def", "tok123", "session=secret", "user:pass", "notify-key"} {
		if strings.Contains(output, secret) {
			t.Fatalf("redacted output contains %q: %s", secret, output)
		}
	}
}

func TestRedactingHandlerCleansMessagesAndAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil)))
	logger.Error("request Authorization: Bearer message-token",
		"password", "attribute-password",
		"error", errors.New("Cookie: session=attribute-cookie"),
	)
	text := output.String()
	for _, secret := range []string{"message-token", "attribute-password", "attribute-cookie"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log contains %q: %s", secret, text)
		}
	}
}

func TestRedactAnyUsesSensitiveMapKeys(t *testing.T) {
	value := RedactAny(map[string]any{
		"token":  "plain-token",
		"nested": map[string]any{"password": "plain-password"},
	}).(map[string]any)
	if value["token"] != redacted || value["nested"].(map[string]any)["password"] != redacted {
		t.Fatalf("sensitive map values were not redacted: %#v", value)
	}
}
