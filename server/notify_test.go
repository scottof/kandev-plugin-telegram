package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func newNotifyPlugin(t *testing.T, users ...int64) (*telegramPlugin, *fakeHost, *fakeTelegram) {
	t.Helper()
	b, host, tg := newAlertBot(t, users...)
	p := newTelegramPlugin(context.Background())
	p.UnimplementedPlugin.SetHost(host)
	p.bot = b
	return p, host, tg
}

func notifyRequest(args map[string]any) *pluginsdk.AgentToolRequest {
	return &pluginsdk.AgentToolRequest{
		InvocationID: "inv-1",
		Name:         notifyToolName,
		Arguments:    args,
		Context:      pluginsdk.AgentToolContext{TaskID: "task-1", SessionID: "sess-1", WorkspaceID: "ws-1", Surface: "kanban-task"},
	}
}

func notifyArgsMap(message, key string) map[string]any {
	return map[string]any{"message": message, "idempotency_key": key}
}

func invoke(t *testing.T, p *telegramPlugin, req *pluginsdk.AgentToolRequest) map[string]any {
	t.Helper()
	res, err := p.InvokeAgentTool(context.Background(), req)
	require.NoError(t, err, "tool failures are results, not RPC errors")
	require.NotNil(t, res)
	status, _ := res.StructuredContent["status"].(string)
	require.Equal(t, status != notifyDelivered, res.IsError)
	require.True(t, strings.HasPrefix(res.Text, status+": "), res.Text)
	return res.StructuredContent
}

func TestNotify_DeliversToAllAllowedUsersWithTaskLink(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42, 7)
	out := invoke(t, p, notifyRequest(notifyArgsMap("Deploy finished, 3 tests skipped.", "deploy:1")))

	require.Equal(t, notifyDelivered, out["status"])
	require.Equal(t, []any{int64(42), int64(7)}, out["delivered_to"])
	sent := tg.sentMessages()
	require.Len(t, sent, 2)
	for _, m := range sent {
		require.Equal(t, "Deploy finished, 3 tests skipped.\n\nFrom task: Fix login", m.Msg.Text)
		require.Equal(t, []linkButton{{Text: "Open task", URL: testBase + "/t/task-1"}}, m.Msg.Buttons)
	}
}

func TestNotify_OneRecipient(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42, 7)
	args := notifyArgsMap("Only for you.", "k1")
	args["recipient"] = float64(7)
	out := invoke(t, p, notifyRequest(args))
	require.Equal(t, notifyDelivered, out["status"])
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Equal(t, int64(7), sent[0].ChatID)
}

func TestNotify_RejectsBadCalls(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42)
	cases := map[string]*pluginsdk.AgentToolRequest{}

	unknown := notifyRequest(notifyArgsMap("hi", "k"))
	unknown.Name = "other_tool"
	cases["unknown_tool"] = unknown

	noTask := notifyRequest(notifyArgsMap("hi", "k"))
	noTask.Context.TaskID = ""
	cases["missing_context"] = noTask

	wrongSurface := notifyRequest(notifyArgsMap("hi", "k"))
	wrongSurface.Context.Surface = "external"
	cases["missing_context/surface"] = wrongSurface

	for name, args := range map[string]map[string]any{
		"invalid_arguments/empty":        notifyArgsMap("   ", "k"),
		"invalid_arguments/key":          notifyArgsMap("hi", "-bad"),
		"invalid_arguments/long":         notifyArgsMap(strings.Repeat("x", maxNotifyRunes+1), "k"),
		"invalid_arguments/control":      notifyArgsMap("hi\x07", "k"),
		"invalid_arguments/extra":        {"message": "hi", "idempotency_key": "k", "chat_id": float64(1)},
		"invalid_arguments/stranger":     {"message": "hi", "idempotency_key": "k", "recipient": float64(999)},
		"invalid_arguments/fraction":     {"message": "hi", "idempotency_key": "k", "recipient": float64(42.5)},
		"invalid_arguments/string_id":    {"message": "hi", "idempotency_key": "k", "recipient": "42"},
		"invalid_arguments/missing_key":  {"message": "hi"},
		"invalid_arguments/missing_text": {"idempotency_key": "k"},
	} {
		cases[name] = notifyRequest(args)
	}

	for name, req := range cases {
		out := invoke(t, p, req)
		require.Equal(t, notifyRejected, out["status"], name)
		require.Equal(t, strings.Split(name, "/")[0], out["code"], name)
	}
	require.Empty(t, tg.sentMessages())
}

func TestNotify_BotNotRunning(t *testing.T) {
	p := newTelegramPlugin(context.Background())
	p.UnimplementedPlugin.SetHost(newFakeHost())
	out := invoke(t, p, notifyRequest(notifyArgsMap("hi", "k")))
	require.Equal(t, "bot_not_running", out["code"])
}

func TestNotify_SameKeyReturnsStoredOutcome(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42)
	first := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	second := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))

	require.Equal(t, notifyDelivered, first["status"])
	require.Equal(t, false, first["duplicate"])
	require.Equal(t, notifyDelivered, second["status"])
	require.Equal(t, true, second["duplicate"])
	require.Len(t, tg.sentMessages(), 1, "a repeated key never sends again")
}

func TestNotify_SameKeyDifferentMessageConflicts(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42)
	invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	out := invoke(t, p, notifyRequest(notifyArgsMap("Something else.", "k1")))
	require.Equal(t, "key_conflict", out["code"])
	require.Len(t, tg.sentMessages(), 1)
}

func TestNotify_KeysAreScopedPerWorkspaceAndRecipient(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42, 7)
	invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))

	other := notifyRequest(notifyArgsMap("Done.", "k1"))
	other.Context.WorkspaceID = "ws-2"
	require.Equal(t, false, invoke(t, p, other)["duplicate"])

	single := notifyArgsMap("Done.", "k1")
	single["recipient"] = float64(42)
	require.Equal(t, false, invoke(t, p, notifyRequest(single))["duplicate"])
	require.Len(t, tg.sentMessages(), 5)
}

func TestNotify_PendingRecordIsNeverResent(t *testing.T) {
	p, host, tg := newNotifyPlugin(t, 42)
	key := notifyStateKey("ws-1", "all", "k1")
	require.NoError(t, host.SetState(context.Background(), notifyStateScope, "ws-1", key, map[string]any{
		"digest": sha256Hex("Done."), "status": notifyPending,
	}))
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, notifyUncertain, out["status"])
	require.Equal(t, "unconfirmed_previous_attempt", out["code"])
	require.Empty(t, tg.sentMessages())
}

func TestNotify_PartialAndFailedDelivery(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42, 7)
	tg.sendErrTo = map[int64]error{7: &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden: bot can't initiate conversation"}}
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, notifyPartial, out["status"])
	require.Equal(t, []any{int64(42)}, out["delivered_to"])
	require.Equal(t, []any{int64(7)}, out["failed_for"])

	tg.sendErrTo[42] = &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden"}
	out = invoke(t, p, notifyRequest(notifyArgsMap("Again.", "k2")))
	require.Equal(t, notifyFailed, out["status"])
	require.Contains(t, out["detail"], "/start")
}

func TestNotify_TransportErrorIsUncertain(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42)
	tg.sendErr = errors.New("telegram sendMessage: connection reset")
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, notifyUncertain, out["status"])
	require.Equal(t, []any{int64(42)}, out["uncertain_for"])
}

func TestNotify_RateLimitHoldSendsNothing(t *testing.T) {
	p, host, tg := newNotifyPlugin(t, 42)
	tg.hold = 61 * time.Second
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, "rate_limited", out["code"])
	require.Equal(t, 61, out["retry_after_seconds"])
	require.Empty(t, tg.sentMessages())
	_, found, err := host.GetState(context.Background(), notifyStateScope, "ws-1", notifyStateKey("ws-1", "all", "k1"))
	require.NoError(t, err)
	require.False(t, found, "the key stays free for a retry")
}

func TestNotify_ReceiptNotSavedIsUncertain(t *testing.T) {
	p, host, tg := newNotifyPlugin(t, 42)
	key := notifyStateKey("ws-1", "all", "k1")
	tg.onSend = func() {
		host.mu.Lock()
		host.setStateErr = map[string]error{key: errors.New("state unavailable")}
		host.mu.Unlock()
	}
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, notifyUncertain, out["status"])
	require.Equal(t, "receipt_not_saved", out["code"])

	tg.onSend = nil
	host.mu.Lock()
	host.setStateErr = nil
	host.mu.Unlock()
	again := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, "unconfirmed_previous_attempt", again["code"], "the pending reservation blocks a resend")
	require.Len(t, tg.sentMessages(), 1)
}

func TestNotify_RefusedButtonFallsBackToPlainText(t *testing.T) {
	p, _, tg := newNotifyPlugin(t, 42)
	tg.sendErrWithButtons = badRequest("Bad Request: inline keyboard button URL is invalid")
	out := invoke(t, p, notifyRequest(notifyArgsMap("Done.", "k1")))
	require.Equal(t, notifyDelivered, out["status"])
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Empty(t, sent[0].Msg.Buttons)
}
