package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func validConfig() map[string]any {
	return map[string]any{
		"bot_token":        testToken,
		"allowed_user_ids": "42",
		"kandev_url":       testBase,
	}
}

func newTestPlugin(ctx context.Context, tg *fakeTelegram) (*telegramPlugin, *atomic.Int32) {
	var created atomic.Int32
	p := newTelegramPlugin(ctx)
	p.clock = newFakeClock()
	p.newClient = func(token string) telegramClient {
		created.Add(1)
		return tg
	}
	return p, &created
}

func TestOnEvent_NoHost_ReturnsNil(t *testing.T) {
	p := &telegramPlugin{}
	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventID: "e1", EventType: "task.state_changed"})
	require.NoError(t, err)
}

func TestSetHost_MissingSettingsLeavesBotIdleWithReason(t *testing.T) {
	host := newFakeHost()
	p, created := newTestPlugin(context.Background(), newFakeTelegram())
	p.SetHost(host)
	p.wait(2 * time.Second)

	require.Equal(t, int32(0), created.Load())
	status, err := stateStore{host: host}.botStatus(context.Background())
	require.NoError(t, err)
	require.False(t, status.Connected)
	require.Equal(t, "missing settings: bot_token, kandev_url", status.LastError)
}

func TestSetHost_InvalidSettingsLeavesBotIdleWithReason(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	host.config["allowed_user_ids"] = "42,nope"
	p, created := newTestPlugin(context.Background(), newFakeTelegram())
	p.SetHost(host)
	p.wait(2 * time.Second)

	require.Equal(t, int32(0), created.Load())
	status, err := stateStore{host: host}.botStatus(context.Background())
	require.NoError(t, err)
	require.Contains(t, status.LastError, "allowed_user_ids")
}

func TestSetHost_StartsOnceAndStopsOnCancel(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	tg := newFakeTelegram()
	ctx, cancel := context.WithCancel(context.Background())
	polling := make(chan struct{})
	tg.onDrained = func() { close(polling) }
	p, created := newTestPlugin(ctx, tg)

	p.SetHost(host)
	p.SetHost(host)
	select {
	case <-polling:
	case <-time.After(2 * time.Second):
		t.Fatal("poll loop did not start")
	}
	require.Equal(t, int32(1), created.Load())
	require.NotNil(t, p.currentBot())

	status, err := stateStore{host: host}.botStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.Connected)
	require.Equal(t, "kandev_bot", status.Username)

	cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("bot did not stop after cancel")
	}
}

func TestSetHost_RetriesGetMeAndRecordsError(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	tg := newFakeTelegram()
	tg.meErr = errors.New("telegram getMe: 401 Unauthorized")
	ctx, cancel := context.WithCancel(context.Background())
	p, _ := newTestPlugin(ctx, tg)
	clk := p.clock.(*fakeClock)
	clk.onSleep = cancel

	p.SetHost(host)
	p.wait(2 * time.Second)

	status, err := stateStore{host: host}.botStatus(context.Background())
	require.NoError(t, err)
	require.Contains(t, status.LastError, "401")
	require.Nil(t, p.currentBot())
}

func webhook(t *testing.T, p *telegramPlugin, key, method string) (int32, map[string]any) {
	t.Helper()
	resp, err := p.HandleWebhook(context.Background(), &pluginsdk.WebhookRequest{WebhookKey: key, Method: method})
	require.NoError(t, err)
	require.Equal(t, "application/json", resp.Headers["Content-Type"])
	var body map[string]any
	require.NoError(t, json.Unmarshal(resp.Body, &body))
	require.NotContains(t, string(resp.Body), testToken)
	return resp.Status, body
}

func TestWebhookStatus_NotConfigured(t *testing.T) {
	p := &telegramPlugin{}
	p.UnimplementedPlugin.SetHost(newFakeHost())
	status, body := webhook(t, p, "status", "GET")
	require.Equal(t, int32(200), status)
	require.Equal(t, false, body["configured"])
	require.Equal(t, []any{"bot_token", "kandev_url"}, body["missing"])
	require.Equal(t, false, body["running"])
}

func TestWebhookStatus_InvalidConfig(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	host.config["kandev_url"] = "kandev.local"
	p := &telegramPlugin{}
	p.UnimplementedPlugin.SetHost(host)
	_, body := webhook(t, p, "status", "GET")
	require.Equal(t, false, body["configured"])
	require.Contains(t, body["config_error"], "kandev_url")
}

func TestWebhookStatus_Running(t *testing.T) {
	b, host, _ := newAlertBot(t, 42, 43)
	host.config = validConfig()
	require.NoError(t, b.store.setBotStatus(context.Background(), botStatus{Connected: true, Username: "kandev_bot", LastUpdateAt: "2026-10-18T10:00:00Z"}))
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)

	status, body := webhook(t, p, "status", "GET")
	require.Equal(t, int32(200), status)
	require.Equal(t, true, body["configured"])
	require.Equal(t, true, body["running"])
	require.Equal(t, true, body["connected"])
	require.Equal(t, "kandev_bot", body["username"])
	require.Equal(t, "2026-10-18T10:00:00Z", body["last_update_at"])
	require.InEpsilon(t, float64(1), body["allowed_users"], 0)
	require.Equal(t, []any{}, body["warnings"])
}

func TestWebhookStatus_WarnsOnLoopbackKandevURL(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	host.config["kandev_url"] = "http://localhost:3000"
	p := &telegramPlugin{}
	p.UnimplementedPlugin.SetHost(host)
	_, body := webhook(t, p, "status", "GET")
	require.Equal(t, true, body["configured"], "a loopback URL is a warning, not an error")
	warnings, _ := body["warnings"].([]any)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "kandev_url")
}

func TestWebhookStatus_NoHost(t *testing.T) {
	status, body := webhook(t, &telegramPlugin{}, "status", "GET")
	require.Equal(t, int32(503), status)
	require.Contains(t, body["error"], "not ready")
}

func TestWebhookTest_SendsToAllowedUsers(t *testing.T) {
	b, host, tg := newAlertBot(t, 42, 43)
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)

	status, body := webhook(t, p, "test", "POST")
	require.Equal(t, int32(200), status)
	require.Equal(t, true, body["ok"])
	require.Equal(t, "kandev_bot", body["username"])
	require.InEpsilon(t, float64(2), body["sent"], 0)
	sent := tg.sentMessages()
	require.Len(t, sent, 2)
	require.Equal(t, "Test message from kandev. Alerts will arrive in this chat.", sent[0].Msg.Text)
}

func TestWebhookTest_ReportsUnreachableUsers(t *testing.T) {
	b, host, tg := newAlertBot(t, 42, 43)
	tg.sendErrTo = map[int64]error{
		42: &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden"},
		43: &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden"},
	}
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)

	status, body := webhook(t, p, "test", "POST")
	require.Equal(t, int32(200), status)
	require.Equal(t, false, body["ok"])
	require.Equal(t, "Could not message 42, 43. Each allowed user must send /start to @kandev_bot first.", body["error"])
}

func TestWebhookTest_FailsFastDuringRetryAfterHold(t *testing.T) {
	b, host, tg := newAlertBot(t, 42)
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)

	tg.setHold(90*time.Second + 200*time.Millisecond)
	status, body := webhook(t, p, "test", "POST")
	require.Equal(t, int32(200), status)
	require.Equal(t, false, body["ok"])
	require.Equal(t, "Telegram asked to wait 91 s before the next message. Try again then.", body["error"])
	require.Zero(t, tg.sendAttempts())

	tg.setHold(0)
	_, body = webhook(t, p, "test", "POST")
	require.Equal(t, true, body["ok"])
	require.Len(t, tg.sentMessages(), 1)
}

func TestWebhookTest_NoAllowedUsers(t *testing.T) {
	b, host, _ := newAlertBot(t)
	b.cfg.AllowedUserIDs = nil
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)
	_, body := webhook(t, p, "test", "POST")
	require.Equal(t, false, body["ok"])
	require.Equal(t, "No allowed user ids are set.", body["error"])
}

func TestWebhookTest_GetMeFailure(t *testing.T) {
	b, host, tg := newAlertBot(t)
	tg.meErr = &telegramAPIError{Method: "getMe", Code: 401, Description: "Unauthorized"}
	p := &telegramPlugin{bot: b}
	p.UnimplementedPlugin.SetHost(host)
	_, body := webhook(t, p, "test", "POST")
	require.Equal(t, false, body["ok"])
	require.Equal(t, "telegram getMe: 401 Unauthorized", body["error"])
	require.Empty(t, tg.sentMessages())
}

func TestWebhookTest_BotNotRunning(t *testing.T) {
	host := newFakeHost()
	require.NoError(t, stateStore{host: host}.setBotStatus(context.Background(), botStatus{LastError: "missing settings: bot_token"}))
	p := &telegramPlugin{}
	p.UnimplementedPlugin.SetHost(host)
	status, body := webhook(t, p, "test", "POST")
	require.Equal(t, int32(409), status)
	require.Equal(t, "The bot is not running: missing settings: bot_token", body["error"])
}

func TestWebhook_WrongMethodAndUnknownKey(t *testing.T) {
	p := &telegramPlugin{}
	p.UnimplementedPlugin.SetHost(newFakeHost())
	status, _ := webhook(t, p, "status", "POST")
	require.Equal(t, int32(405), status)
	status, _ = webhook(t, p, "test", "GET")
	require.Equal(t, int32(405), status)
	status, _ = webhook(t, p, "ping", "POST")
	require.Equal(t, int32(404), status)
}

func TestOnEvent_WhileStartingAsksForRedelivery(t *testing.T) {
	p := newTelegramPlugin(context.Background())
	p.UnimplementedPlugin.SetHost(newFakeHost())
	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventType: "task.state_changed"})
	require.ErrorIs(t, err, errBotStarting)
}

func TestOnEvent_IdleBotDropsEvents(t *testing.T) {
	host := newFakeHost()
	p, _ := newTestPlugin(context.Background(), newFakeTelegram())
	p.SetHost(host)
	p.wait(2 * time.Second)
	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventType: "task.state_changed"})
	require.NoError(t, err, "a bot left idle by missing settings never retries")
}

func TestSetHost_UnauthorizedTokenGoesIdleWithoutRetrying(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	tg := newFakeTelegram()
	tg.meErr = &telegramAPIError{Method: "getMe", Code: 401, Description: "Unauthorized"}
	p, _ := newTestPlugin(context.Background(), tg)

	p.SetHost(host)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("a rejected token kept the bot retrying")
	}
	require.Empty(t, p.clock.(*fakeClock).sleeps())
	require.Nil(t, p.currentBot())

	status, err := stateStore{host: host}.botStatus(context.Background())
	require.NoError(t, err)
	require.False(t, status.Connected)
	require.Equal(t, "telegram getMe: 401 Unauthorized", status.LastError)

	err = p.OnEvent(context.Background(), &pluginsdk.Event{EventType: "task.state_changed"})
	require.NoError(t, err, "events are not redelivered to a bot that will not start")
}
