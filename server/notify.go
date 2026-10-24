package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	notifyToolName = "notify_user"
	// maxNotifyRunes leaves room for the task line inside Telegram's 4096.
	maxNotifyRunes = 3500
	// kandev gives a tool call 30 seconds; the send gets 20 so the outcome
	// can still be saved.
	notifySendTimeout = 20 * time.Second
	notifySaveTimeout = 3 * time.Second
	notifyStateScope  = "workspace"

	notifyDelivered = "delivered"
	notifyPartial   = "partial"
	notifyFailed    = "failed"
	notifyUncertain = "uncertain"
	notifyRejected  = "rejected"
	notifyPending   = "pending"
)

var notifyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

var _ pluginsdk.AgentToolPlugin = (*telegramPlugin)(nil)

// InvokeAgentTool serves notify_user. The task, session and workspace come
// only from the host-verified context, never from the arguments.
func (p *telegramPlugin) InvokeAgentTool(ctx context.Context, req *pluginsdk.AgentToolRequest) (*pluginsdk.AgentToolResult, error) {
	if req == nil || req.Name != notifyToolName {
		return notifyRejection("unknown_tool", "Unknown tool."), nil
	}
	c := req.Context
	if c.TaskID == "" || c.WorkspaceID == "" || (c.Surface != "kanban-task" && c.Surface != "office-task") {
		return notifyRejection("missing_context", "A host-verified task and workspace are required."), nil
	}
	b := p.currentBot()
	if b == nil {
		return notifyRejection("bot_not_running", "The Telegram bot is not connected. Check Settings > Plugins > Telegram. Nothing was sent."), nil
	}
	args, problem := parseNotifyArgs(req.Arguments, b.cfg.AllowedUserIDs)
	if problem != "" {
		return notifyRejection("invalid_arguments", problem), nil
	}
	return b.notify(ctx, c, args), nil
}

type notifyArgs struct {
	message    string
	key        string
	recipients []int64
	// recipientKey is part of the idempotency identity: "all" or the user id.
	recipientKey string
}

func parseNotifyArgs(raw map[string]any, allowed []int64) (notifyArgs, string) {
	const usage = "Use message (1 to 3500 characters of plain text), idempotency_key (1 to 128 characters: letters, " +
		"digits, and . _ : / - after the first), and optionally recipient (one allowed Telegram user id)."
	for name := range raw {
		if name != "message" && name != "idempotency_key" && name != "recipient" {
			return notifyArgs{}, usage
		}
	}
	message, _ := raw["message"].(string)
	key, _ := raw["idempotency_key"].(string)
	message = strings.TrimSpace(message)
	if message == "" || !utf8.ValidString(message) || utf8.RuneCountInString(message) > maxNotifyRunes || !notifyKeyPattern.MatchString(key) {
		return notifyArgs{}, usage
	}
	for _, r := range message {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return notifyArgs{}, usage
		}
	}
	args := notifyArgs{message: message, key: key, recipients: allowed, recipientKey: "all"}
	if rawRecipient, ok := raw["recipient"]; ok {
		n, isNumber := rawRecipient.(float64)
		if !isNumber || n <= 0 || n != math.Trunc(n) {
			return notifyArgs{}, usage
		}
		id := int64(n)
		if !containsID(allowed, id) {
			return notifyArgs{}, "recipient is not one of the allowed Telegram users. Omit it to message all of them."
		}
		args.recipients = []int64{id}
		args.recipientKey = fmt.Sprint(id)
	}
	if len(args.recipients) == 0 {
		return notifyArgs{}, "No Telegram users are allowed yet. Add them in Settings > Plugins > Telegram."
	}
	return args, ""
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// notify sends args once per idempotency key. A repeated key returns the
// stored outcome. A record left pending, for example by a crash between
// Telegram accepting the message and saving the receipt, is reported as
// uncertain and never resent.
func (b *bot) notify(ctx context.Context, c pluginsdk.AgentToolContext, args notifyArgs) *pluginsdk.AgentToolResult {
	b.notifyMu.Lock()
	defer b.notifyMu.Unlock()

	if hold := b.tg.HoldRemaining(); hold > 0 {
		seconds := int(math.Ceil(hold.Seconds()))
		return notifyResultFrom(map[string]any{
			"status": notifyFailed, "code": "rate_limited", "duplicate": false, "retry_after_seconds": seconds,
			"detail": fmt.Sprintf("Telegram asked the bot to wait %d s. Nothing was sent; retry later with the same key.", seconds),
		})
	}

	stateKey := notifyStateKey(c.WorkspaceID, args.recipientKey, args.key)
	digest := sha256Hex(args.message)
	record, found, err := b.host.GetState(ctx, notifyStateScope, c.WorkspaceID, stateKey)
	if err != nil {
		return notifyRejection("state_unavailable", "Cannot read the notification record. Nothing was sent.")
	}
	if found {
		return storedNotifyOutcome(record, digest)
	}

	reservation := map[string]any{"digest": digest, "status": notifyPending, "created_at": b.now()}
	if err := b.host.SetState(ctx, notifyStateScope, c.WorkspaceID, stateKey, reservation); err != nil {
		return notifyRejection("state_unavailable", "Cannot reserve the notification record. Nothing was sent.")
	}

	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	result := b.deliverNotification(sendCtx, c.TaskID, args)
	cancel()

	reservation["status"] = result["status"]
	reservation["result"] = result
	// Save even if the call was cancelled after sending: a lost receipt would
	// turn into a resend. If saving fails, the pending record blocks repeats.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), notifySaveTimeout)
	defer saveCancel()
	if err := b.host.SetState(saveCtx, notifyStateScope, c.WorkspaceID, stateKey, reservation); err != nil {
		result["status"] = notifyUncertain
		result["code"] = "receipt_not_saved"
		result["detail"] = "The outcome could not be saved. Do not retry with a new key until someone checks Telegram."
	}
	return notifyResultFrom(result)
}

func storedNotifyOutcome(record map[string]any, digest string) *pluginsdk.AgentToolResult {
	if stored, _ := record["digest"].(string); stored != digest {
		return notifyRejection("key_conflict", "This idempotency key was already used for a different message. Nothing was sent.")
	}
	if result, ok := record["result"].(map[string]any); ok {
		copied := make(map[string]any, len(result)+1)
		for k, v := range result {
			copied[k] = v
		}
		copied["duplicate"] = true
		return notifyResultFrom(copied)
	}
	return notifyResultFrom(map[string]any{
		"status": notifyUncertain, "code": "unconfirmed_previous_attempt", "duplicate": true,
		"detail": "A previous attempt with this key has no confirmed outcome. Do not retry with a new key until someone checks Telegram.",
	})
}

// deliverNotification sends the message to each recipient with a line naming
// the task and a link button, which falls back to plain text when refused.
func (b *bot) deliverNotification(ctx context.Context, taskID string, args notifyArgs) map[string]any {
	title := untitledTask
	if task, err := b.host.Tasks().Get(ctx, taskID); err == nil && task != nil && task.Title != "" {
		title = task.Title
	}
	msg := outMessage{
		Text:    capMessage(args.message + "\n\nFrom task: " + truncateRunes(title, maxTitleRunes)),
		Buttons: []linkButton{{Text: "Open task", URL: taskURL(b.cfg.KandevURL, taskID)}},
	}

	var delivered, failed, uncertain []any
	for _, userID := range args.recipients {
		_, err := b.sendWithFallback(ctx, userID, msg, 0)
		var apiErr *telegramAPIError
		switch {
		case err == nil:
			delivered = append(delivered, userID)
		case errors.As(err, &apiErr):
			// Telegram answered, so the message was not sent.
			failed = append(failed, userID)
		default:
			// A transport error or timeout may hide a delivered message.
			uncertain = append(uncertain, userID)
		}
	}

	status, code, detail := notifyDelivered, "ok", "Sent."
	switch {
	case len(uncertain) > 0:
		status, code = notifyUncertain, "unconfirmed"
		detail = "Telegram did not confirm every message. Do not retry with a new key until someone checks Telegram."
	case len(failed) > 0 && len(delivered) > 0:
		status, code, detail = notifyPartial, "some_failed", "Telegram refused the message for some users. It was not resent."
	case len(failed) > 0:
		status, code = notifyFailed, "telegram_refused"
		detail = "Telegram refused the message. Users must send /start to the bot once before it can message them."
	}
	return map[string]any{
		"status": status, "code": code, "detail": detail, "duplicate": false,
		"delivered_to": orEmpty(delivered), "failed_for": orEmpty(failed), "uncertain_for": orEmpty(uncertain),
	}
}

func orEmpty(ids []any) []any {
	if ids == nil {
		return []any{}
	}
	return ids
}

// notifyStateKey hashes the identity tuple so user-chosen keys cannot collide
// by concatenation.
func notifyStateKey(workspaceID, recipientKey, key string) string {
	tuple, _ := json.Marshal([]string{workspaceID, recipientKey, key})
	return fmt.Sprintf("notify.v1.%x", sha256.Sum256(tuple))
}

func sha256Hex(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
}

func notifyRejection(code, detail string) *pluginsdk.AgentToolResult {
	return notifyResultFrom(map[string]any{"status": notifyRejected, "code": code, "detail": detail, "duplicate": false})
}

func notifyResultFrom(result map[string]any) *pluginsdk.AgentToolResult {
	status, _ := result["status"].(string)
	detail, _ := result["detail"].(string)
	return &pluginsdk.AgentToolResult{
		Text:              status + ": " + detail,
		StructuredContent: result,
		IsError:           status != notifyDelivered,
	}
}
