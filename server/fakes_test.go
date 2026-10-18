package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is an in-memory pluginsdk.Host. SetState round-trips values through
// JSON like the real Host's protobuf Struct, so numbers come back as float64.
type fakeHost struct {
	pluginsdk.UnimplementedHostData
	mu     sync.Mutex
	state  map[string]map[string]any
	config map[string]any
}

func newFakeHost() *fakeHost {
	return &fakeHost{state: make(map[string]map[string]any), config: map[string]any{}}
}

func fakeStateKey(scope, scopeID, key string) string {
	return scope + "/" + scopeID + "/" + key
}

func (h *fakeHost) GetState(_ context.Context, scope, scopeID, key string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.state[fakeStateKey(scope, scopeID, key)]
	return v, ok, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, scopeID, key string, value map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state[fakeStateKey(scope, scopeID, key)] = jsonRoundTrip(value)
	return nil
}

func (h *fakeHost) DeleteState(_ context.Context, scope, scopeID, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, fakeStateKey(scope, scopeID, key))
	return nil
}

func (h *fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}

func (h *fakeHost) GetConfig(context.Context) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.config, nil
}

func (h *fakeHost) RevealSecret(context.Context, string) (string, error) { return "", nil }
func (h *fakeHost) GetSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (h *fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (h *fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (h *fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

var _ pluginsdk.Host = (*fakeHost)(nil)

// fakeClock records sleeps instead of waiting, and advances Now by each sleep.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	slept   []time.Duration
	onSleep func()
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	hook := c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ctx.Err()
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// fakeTelegram is an in-memory telegramClient. GetUpdates serves queued
// results in order; once the queue is empty it calls onDrained and then blocks
// like a long poll until the context ends.
type fakeTelegram struct {
	mu        sync.Mutex
	me        tgUser
	meErr     error
	queue     []fakeUpdatesResult
	polled    []int64
	sent      []sentMessage
	sendErr   error
	sendErrTo map[int64]error
	onSend    func()
	onDrained func()
}

type fakeUpdatesResult struct {
	updates []tgUpdate
	err     error
}

type sentMessage struct {
	ChatID  int64
	Msg     outMessage
	ReplyTo int64
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{me: tgUser{ID: 99, IsBot: true, FirstName: "Kandev", Username: "kandev_bot"}}
}

func (f *fakeTelegram) queueUpdates(updates ...tgUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, fakeUpdatesResult{updates: updates})
}

func (f *fakeTelegram) queueError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, fakeUpdatesResult{err: err})
}

func (f *fakeTelegram) GetMe(context.Context) (tgUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.me, f.meErr
}

func (f *fakeTelegram) GetUpdates(ctx context.Context, offset int64, _ int) ([]tgUpdate, error) {
	f.mu.Lock()
	f.polled = append(f.polled, offset)
	if len(f.queue) > 0 {
		next := f.queue[0]
		f.queue = f.queue[1:]
		f.mu.Unlock()
		return next.updates, next.err
	}
	drained := f.onDrained
	f.mu.Unlock()
	if drained != nil {
		drained()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID int64, msg outMessage, replyTo int64) error {
	f.mu.Lock()
	hook := f.onSend
	err := f.sendErr
	if e, ok := f.sendErrTo[chatID]; ok {
		err = e
	}
	if err == nil {
		f.sent = append(f.sent, sentMessage{ChatID: chatID, Msg: msg, ReplyTo: replyTo})
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeTelegram) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeTelegram) offsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.polled...)
}

var _ telegramClient = (*fakeTelegram)(nil)

func jsonRoundTrip(v map[string]any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}
