package main

import (
	"context"
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
