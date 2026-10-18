package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const testMessageText = "Test message from kandev. Alerts will arrive in this chat."

// HandleWebhook serves the settings card. Both webhooks are declared with
// access: authenticated, so only a signed-in kandev user reaches them.
func (p *telegramPlugin) HandleWebhook(ctx context.Context, req *pluginsdk.WebhookRequest) (*pluginsdk.WebhookResponse, error) {
	host := p.Host()
	if host == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "the plugin is not ready yet"}), nil
	}
	switch req.WebhookKey {
	case "status":
		if req.Method != http.MethodGet {
			return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "use GET"}), nil
		}
		return p.statusWebhook(ctx, host), nil
	case "test":
		if req.Method != http.MethodPost {
			return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "use POST"}), nil
		}
		return p.testWebhook(ctx, host), nil
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "unknown webhook"}), nil
	}
}

// statusWebhook reports configuration and connection state, never the token.
func (p *telegramPlugin) statusWebhook(ctx context.Context, host pluginsdk.Host) *pluginsdk.WebhookResponse {
	body := map[string]any{"configured": false, "missing": []string{}, "warnings": []string{}, "running": p.currentBot() != nil}

	raw, err := host.GetConfig(ctx)
	if err != nil {
		body["config_error"] = "could not read settings"
	} else if cfg, err := loadConfig(raw); err != nil {
		body["config_error"] = err.Error()
	} else {
		missing := cfg.missingSettings()
		if missing == nil {
			missing = []string{}
		}
		body["missing"] = missing
		body["configured"] = len(missing) == 0
		body["allowed_users"] = len(cfg.AllowedUserIDs)
		if warnings := cfg.warnings(); warnings != nil {
			body["warnings"] = warnings
		}
	}

	st, err := stateStore{host: host}.botStatus(ctx)
	if err == nil {
		body["connected"] = st.Connected
		body["username"] = st.Username
		body["last_update_at"] = st.LastUpdateAt
		body["last_error"] = st.LastError
		body["alert_error"] = st.AlertError
	}
	return jsonResponse(http.StatusOK, body)
}

// testWebhook checks the token with getMe and sends one message to every
// allowed user, reporting who could not be reached.
func (p *telegramPlugin) testWebhook(ctx context.Context, host pluginsdk.Host) *pluginsdk.WebhookResponse {
	b := p.currentBot()
	if b == nil {
		reason := "it is still starting"
		if st, err := (stateStore{host: host}).botStatus(ctx); err == nil && st.LastError != "" {
			reason = st.LastError
		}
		return jsonResponse(http.StatusConflict, map[string]any{"ok": false, "error": "The bot is not running: " + reason})
	}

	// Waiting out retry_after here would outlast the webhook request.
	if wait := b.tg.HoldRemaining(); wait > 0 {
		seconds := int64(math.Ceil(wait.Seconds()))
		return jsonResponse(http.StatusOK, map[string]any{"ok": false, "error": fmt.Sprintf("Telegram asked to wait %d s before the next message. Try again then.", seconds)})
	}

	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return jsonResponse(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	if len(b.cfg.AllowedUserIDs) == 0 {
		return jsonResponse(http.StatusOK, map[string]any{"ok": false, "username": me.Username, "error": "No allowed user ids are set."})
	}

	sent := 0
	var failed []string
	for _, userID := range b.cfg.AllowedUserIDs {
		if err := b.tg.SendMessage(ctx, userID, outMessage{Text: testMessageText}, 0); err != nil {
			failed = append(failed, strconv.FormatInt(userID, 10))
			continue
		}
		sent++
	}
	body := map[string]any{"ok": len(failed) == 0, "username": me.Username, "sent": sent}
	if len(failed) > 0 {
		body["error"] = "Could not message " + strings.Join(failed, ", ") + ". Each allowed user must send /start to @" + me.Username + " first."
	}
	return jsonResponse(http.StatusOK, body)
}

func jsonResponse(status int, body map[string]any) *pluginsdk.WebhookResponse {
	data, err := json.Marshal(body)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"encoding response"}`)
	}
	return &pluginsdk.WebhookResponse{
		Status:  int32(status),
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    data,
	}
}
