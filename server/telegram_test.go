package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testToken = "123456:SECRET-token"

// stubTelegram is a loopback Bot API. handler answers one method call and
// returns the JSON body to send back.
type stubTelegram struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []stubCall
	handlers map[string]func(body map[string]any) (int, string)
}

type stubCall struct {
	Method string
	Body   map[string]any
}

func newStubTelegram(t *testing.T) (*stubTelegram, *httptest.Server) {
	stub := &stubTelegram{t: t, handlers: map[string]func(map[string]any) (int, string){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/bot" + testToken + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		var body map[string]any
		if r.ContentLength > 0 {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		}
		stub.mu.Lock()
		stub.calls = append(stub.calls, stubCall{Method: method, Body: body})
		handler := stub.handlers[method]
		stub.mu.Unlock()
		if handler == nil {
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		status, resp := handler(body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return stub, srv
}

func (s *stubTelegram) recorded() []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubCall(nil), s.calls...)
}

func newTestClient(srv *httptest.Server, clk clock) *httpTelegram {
	return &httpTelegram{baseURL: srv.URL, token: testToken, http: srv.Client(), clock: clk}
}

func TestHTTPTelegram_GetMe(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["getMe"] = func(map[string]any) (int, string) {
		return 200, `{"ok":true,"result":{"id":99,"is_bot":true,"first_name":"Kandev","username":"kandev_bot"}}`
	}
	me, err := newTestClient(srv, newFakeClock()).GetMe(context.Background())
	require.NoError(t, err)
	require.Equal(t, tgUser{ID: 99, IsBot: true, FirstName: "Kandev", Username: "kandev_bot"}, me)
}

func TestHTTPTelegram_GetUpdatesRequestAndDecode(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["getUpdates"] = func(map[string]any) (int, string) {
		return 200, `{"ok":true,"result":[{"update_id":10,"message":{"message_id":5,"date":1,"text":"/status",
			"from":{"id":42,"is_bot":false,"first_name":"M"},"chat":{"id":42,"type":"private"},
			"reply_to_message":{"message_id":4,"date":1,"text":"earlier","chat":{"id":42,"type":"private"}}}}]}`
	}
	updates, err := newTestClient(srv, newFakeClock()).GetUpdates(context.Background(), 10, 30)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, int64(10), updates[0].UpdateID)
	msg := updates[0].Message
	require.NotNil(t, msg)
	require.Equal(t, "/status", msg.Text)
	require.Equal(t, int64(42), msg.From.ID)
	require.Equal(t, "private", msg.Chat.Type)
	require.Equal(t, "earlier", msg.ReplyToMessage.Text)

	body := stub.recorded()[0].Body
	require.InEpsilon(t, float64(10), body["offset"], 0)
	require.InEpsilon(t, float64(30), body["timeout"], 0)
	require.Equal(t, []any{"message"}, body["allowed_updates"])
}

func TestHTTPTelegram_SendMessageBody(t *testing.T) {
	stub, srv := newStubTelegram(t)
	err := newTestClient(srv, newFakeClock()).SendMessage(context.Background(), 42, outMessage{
		Text:    "Finished: Fix login",
		Buttons: []linkButton{{Text: "Open task", URL: "https://k/t/1"}},
	}, 7)
	require.NoError(t, err)

	call := stub.recorded()[0]
	require.Equal(t, "sendMessage", call.Method)
	require.InEpsilon(t, float64(42), call.Body["chat_id"], 0)
	require.Equal(t, "Finished: Fix login", call.Body["text"])
	require.NotContains(t, call.Body, "parse_mode")
	require.Equal(t, map[string]any{"is_disabled": true}, call.Body["link_preview_options"])
	require.Equal(t, map[string]any{"message_id": float64(7)}, call.Body["reply_parameters"])
	require.Equal(t, map[string]any{"inline_keyboard": []any{
		[]any{map[string]any{"text": "Open task", "url": "https://k/t/1"}},
	}}, call.Body["reply_markup"])
}

func TestHTTPTelegram_SendMessageWithoutButtonsOrReply(t *testing.T) {
	stub, srv := newStubTelegram(t)
	require.NoError(t, newTestClient(srv, newFakeClock()).SendMessage(context.Background(), 42, outMessage{Text: "hi"}, 0))
	body := stub.recorded()[0].Body
	require.NotContains(t, body, "reply_markup")
	require.NotContains(t, body, "reply_parameters")
}

func TestHTTPTelegram_RetriesOnceAfterRetryAfter(t *testing.T) {
	stub, srv := newStubTelegram(t)
	attempts := 0
	stub.handlers["sendMessage"] = func(map[string]any) (int, string) {
		attempts++
		if attempts == 1 {
			return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3","parameters":{"retry_after":3}}`
		}
		return 200, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":42,"type":"private"}}}`
	}
	clk := newFakeClock()
	require.NoError(t, newTestClient(srv, clk).SendMessage(context.Background(), 42, outMessage{Text: "hi"}, 0))
	require.Equal(t, 2, attempts)
	require.Equal(t, []time.Duration{3 * time.Second}, clk.sleeps())
}

func TestHTTPTelegram_GivesUpAfterSecond429(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["sendMessage"] = func(map[string]any) (int, string) {
		return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`
	}
	err := newTestClient(srv, newFakeClock()).SendMessage(context.Background(), 42, outMessage{Text: "hi"}, 0)
	var apiErr *telegramAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 429, apiErr.Code)
	require.Len(t, stub.recorded(), 2)
}

func TestHTTPTelegram_APIErrorNeverLeaksToken(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["getUpdates"] = func(map[string]any) (int, string) {
		return 409, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`
	}
	_, err := newTestClient(srv, newFakeClock()).GetUpdates(context.Background(), 0, 30)
	var apiErr *telegramAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 409, apiErr.Code)
	require.NotContains(t, err.Error(), testToken)
	require.NotContains(t, err.Error(), srv.URL)
}

func TestHTTPTelegram_TransportErrorNeverLeaksTokenOrURL(t *testing.T) {
	_, srv := newStubTelegram(t)
	client := newTestClient(srv, newFakeClock())
	srv.Close()
	_, err := client.GetMe(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), testToken)
	require.NotContains(t, err.Error(), "/bot")
	require.NotContains(t, err.Error(), srv.URL)
}

func TestHTTPTelegram_DescriptionContainingTokenIsRedacted(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["getMe"] = func(map[string]any) (int, string) {
		return 401, `{"ok":false,"error_code":401,"description":"Unauthorized ` + testToken + `"}`
	}
	_, err := newTestClient(srv, newFakeClock()).GetMe(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), testToken)
}

func TestHTTPTelegram_ContextCancelStopsRetrySleep(t *testing.T) {
	stub, srv := newStubTelegram(t)
	stub.handlers["sendMessage"] = func(map[string]any) (int, string) {
		return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":30}}`
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newTestClient(srv, realClock{}).SendMessage(ctx, 42, outMessage{Text: "hi"}, 0)
	require.True(t, errors.Is(err, context.Canceled))
}
