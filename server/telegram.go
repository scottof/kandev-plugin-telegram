package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	telegramAPIBase     = "https://api.telegram.org"
	maxResponseBytes    = 8 << 20
	sendRequestTimeout  = 20 * time.Second
	pollRequestHeadroom = 15 * time.Second
	maxRetryAfter       = 5 * time.Minute
)

// clock is injected so retry and backoff waits are testable.
type clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Bot API types, limited to the fields this plugin reads.
type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgMessage struct {
	MessageID      int64      `json:"message_id"`
	From           *tgUser    `json:"from,omitempty"`
	Chat           tgChat     `json:"chat"`
	Date           int64      `json:"date"`
	Text           string     `json:"text,omitempty"`
	ReplyToMessage *tgMessage `json:"reply_to_message,omitempty"`
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message,omitempty"`
}

// telegramClient is the slice of the Bot API the plugin uses.
type telegramClient interface {
	GetMe(ctx context.Context) (tgUser, error)
	GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]tgUpdate, error)
	// SendMessage sends plain text; replyTo of 0 means not a reply.
	SendMessage(ctx context.Context, chatID int64, msg outMessage, replyTo int64) error
	// SetReaction puts one emoji reaction on a message.
	SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error
	// HoldRemaining is how long calls still wait out the last retry_after.
	HoldRemaining() time.Duration
}

// telegramAPIError is a Bot API error rebuilt from the response body only, so
// it never carries the request URL (which contains the token).
type telegramAPIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *telegramAPIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

func isUnauthorized(err error) bool {
	var apiErr *telegramAPIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized
}

// isPermanent reports a Bot API rejection that retrying the same request
// will not fix: any 4xx except 429, which carries retry_after.
func isPermanent(err error) bool {
	var apiErr *telegramAPIError
	return errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != http.StatusTooManyRequests
}

func isBadRequest(err error) bool {
	var apiErr *telegramAPIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest
}

func isForbidden(err error) bool {
	var apiErr *telegramAPIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusForbidden
}

// httpTelegram talks to the Bot API over plain net/http.
type httpTelegram struct {
	baseURL string
	token   string
	http    *http.Client
	clock   clock

	// holdUntil is when the last 429 retry_after ends; no call goes out
	// before it, whatever the method or chat.
	holdMu    sync.Mutex
	holdUntil time.Time
}

func newHTTPTelegram(token string) *httpTelegram {
	return &httpTelegram{baseURL: telegramAPIBase, token: token, http: &http.Client{}, clock: realClock{}}
}

func (c *httpTelegram) GetMe(ctx context.Context) (tgUser, error) {
	var me tgUser
	err := c.call(ctx, "getMe", nil, &me, sendRequestTimeout)
	return me, err
}

func (c *httpTelegram) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]tgUpdate, error) {
	req := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message"},
	}
	var updates []tgUpdate
	err := c.call(ctx, "getUpdates", req, &updates, time.Duration(timeoutSeconds)*time.Second+pollRequestHeadroom)
	return updates, err
}

type tgInlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type tgSendMessage struct {
	ChatID             int64          `json:"chat_id"`
	Text               string         `json:"text"`
	LinkPreviewOptions map[string]any `json:"link_preview_options"`
	ReplyParameters    map[string]any `json:"reply_parameters,omitempty"`
	ReplyMarkup        map[string]any `json:"reply_markup,omitempty"`
}

func (c *httpTelegram) SendMessage(ctx context.Context, chatID int64, msg outMessage, replyTo int64) error {
	req := tgSendMessage{
		ChatID:             chatID,
		Text:               msg.Text,
		LinkPreviewOptions: map[string]any{"is_disabled": true},
	}
	if replyTo != 0 {
		req.ReplyParameters = map[string]any{"message_id": replyTo}
	}
	if len(msg.Buttons) > 0 {
		rows := make([][]tgInlineButton, 0, len(msg.Buttons))
		for _, b := range msg.Buttons {
			rows = append(rows, []tgInlineButton{{Text: b.Text, URL: b.URL}})
		}
		req.ReplyMarkup = map[string]any{"inline_keyboard": rows}
	}
	return c.call(ctx, "sendMessage", req, nil, sendRequestTimeout)
}

func (c *httpTelegram) SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	req := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}
	return c.call(ctx, "setMessageReaction", req, nil, sendRequestTimeout)
}

// call performs one Bot API method, retrying once after a 429 retry_after.
func (c *httpTelegram) call(ctx context.Context, method string, req, result any, timeout time.Duration) error {
	for attempt := 0; ; attempt++ {
		if err := c.waitForHold(ctx); err != nil {
			return err
		}
		err := c.do(ctx, method, req, result, timeout)
		var apiErr *telegramAPIError
		if !errors.As(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests || apiErr.RetryAfter <= 0 {
			return err
		}
		c.hold(time.Duration(apiErr.RetryAfter) * time.Second)
		if attempt > 0 {
			return err
		}
	}
}

func (c *httpTelegram) hold(d time.Duration) {
	// Clamped rather than rejected: a longer retry_after would freeze the
	// bot, and the next 429 sets a fresh hold anyway.
	d = min(d, maxRetryAfter)
	until := c.clock.Now().Add(d)
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	if until.After(c.holdUntil) {
		c.holdUntil = until
	}
}

func (c *httpTelegram) HoldRemaining() time.Duration {
	c.holdMu.Lock()
	until := c.holdUntil
	c.holdMu.Unlock()
	return max(until.Sub(c.clock.Now()), 0)
}

// waitForHold re-reads the hold after every sleep: another call may have
// received a longer retry_after in the meantime.
func (c *httpTelegram) waitForHold(ctx context.Context) error {
	for {
		wait := c.HoldRemaining()
		if wait == 0 {
			return nil
		}
		if err := c.clock.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *httpTelegram) do(ctx context.Context, method string, req, result any, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var body io.Reader
	if req != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("telegram %s: encoding request: %w", method, err)
		}
		body = bytes.NewReader(data)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram %s: building request failed", method)
	}
	if req != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return c.transportError(method, err)
	}
	defer resp.Body.Close()

	var parsed tgResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&parsed); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d with an unreadable body", method, resp.StatusCode)
	}
	if !parsed.OK {
		apiErr := &telegramAPIError{Method: method, Code: parsed.ErrorCode, Description: c.redact(parsed.Description)}
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		if parsed.Parameters != nil {
			apiErr.RetryAfter = parsed.Parameters.RetryAfter
		}
		return apiErr
	}
	if result != nil {
		if err := json.Unmarshal(parsed.Result, result); err != nil {
			return fmt.Errorf("telegram %s: decoding result: %w", method, err)
		}
	}
	return nil
}

// transportError drops the *url.Error wrapper, whose message includes the
// request URL and therefore the token.
func (c *httpTelegram) transportError(method string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("telegram %s: request failed: %s", method, c.redact(err.Error()))
}

func (c *httpTelegram) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "<redacted>")
}
