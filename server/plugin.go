package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// telegramPlugin implements pluginsdk.Plugin. The SDK has no start or stop
// hook: the bot starts once the Host arrives through SetHost, and main cancels
// ctx after pluginsdk.Serve returns. Config changes restart the process.
type telegramPlugin struct {
	pluginsdk.UnimplementedPlugin

	ctx       context.Context
	newClient func(token string) telegramClient
	clock     clock

	startOnce sync.Once
	done      chan struct{}
	// idle is set when the bot will not start in this process, for example
	// because settings are missing.
	idle atomic.Bool

	mu  sync.Mutex
	bot *bot
}

var _ pluginsdk.Plugin = (*telegramPlugin)(nil)

// errBotStarting asks kandev to redeliver an event that arrived before the
// bot connected, instead of losing a task alert.
var errBotStarting = errors.New("telegram bot is still starting")

func newTelegramPlugin(ctx context.Context) *telegramPlugin {
	return &telegramPlugin{
		ctx:       ctx,
		newClient: func(token string) telegramClient { return newHTTPTelegram(token) },
		clock:     realClock{},
		done:      make(chan struct{}),
	}
}

// SetHost is called once by pluginsdk.Serve after the broker dial succeeds.
func (p *telegramPlugin) SetHost(h pluginsdk.Host) {
	p.UnimplementedPlugin.SetHost(h)
	if p.ctx == nil || h == nil {
		return
	}
	p.startOnce.Do(func() { go p.run(p.ctx, h) })
}

// wait blocks until the bot goroutine exits or timeout passes.
func (p *telegramPlugin) wait(timeout time.Duration) {
	if p.done == nil {
		return
	}
	select {
	case <-p.done:
	case <-time.After(timeout):
	}
}

func (p *telegramPlugin) currentBot() *bot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bot
}

// OnEvent handles the events declared in manifest.yaml. Returning an error
// asks kandev to redeliver, which the task alert log makes safe.
func (p *telegramPlugin) OnEvent(ctx context.Context, e *pluginsdk.Event) error {
	if e == nil {
		return nil
	}
	b := p.currentBot()
	if b == nil {
		if p.Host() == nil || p.idle.Load() {
			return nil
		}
		return errBotStarting
	}
	switch e.EventType {
	case eventTaskStateChanged:
		a, ok, err := b.taskAlertFromEvent(ctx, e.Payload)
		if err != nil {
			return err
		}
		if ok {
			b.enqueue(a)
		}
	case eventPendingActionChanged:
		b.nudge()
	}
	return nil
}

func (p *telegramPlugin) run(ctx context.Context, host pluginsdk.Host) {
	defer close(p.done)
	store := stateStore{host: host}

	cfg, err := p.loadConfig(ctx, host)
	if err != nil {
		p.idle.Store(true)
		if ctx.Err() == nil {
			_ = store.setBotStatus(ctx, botStatus{LastError: err.Error()})
		}
		return
	}

	for _, warning := range cfg.warnings() {
		log.Printf("telegram: %s", warning)
	}

	tg := p.newClient(cfg.BotToken)
	me, ok := p.connect(ctx, tg, store)
	if !ok {
		p.idle.Store(true)
		return
	}

	b := newBot(host, tg, cfg, p.clock, me.Username)
	p.mu.Lock()
	p.bot = b
	p.mu.Unlock()
	b.run(ctx)
}

// loadConfig reads and validates the settings. An invalid or incomplete
// config leaves the bot idle with the reason in its status.
func (p *telegramPlugin) loadConfig(ctx context.Context, host pluginsdk.Host) (config, error) {
	var wait backoff
	for {
		raw, err := host.GetConfig(ctx)
		if err == nil {
			cfg, err := loadConfig(raw)
			if err != nil {
				return config{}, err
			}
			if missing := cfg.missingSettings(); len(missing) > 0 {
				return config{}, fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
			}
			return cfg, nil
		}
		log.Printf("telegram: reading plugin config: %v", err)
		if p.clock.Sleep(ctx, wait.next()) != nil {
			return config{}, ctx.Err()
		}
	}
}

// connect calls getMe until the token works, recording failures in status.
func (p *telegramPlugin) connect(ctx context.Context, tg telegramClient, store stateStore) (tgUser, bool) {
	var wait backoff
	for {
		me, err := tg.GetMe(ctx)
		if err == nil {
			return me, true
		}
		if ctx.Err() != nil {
			return tgUser{}, false
		}
		_ = store.setBotStatus(ctx, botStatus{LastError: err.Error()})
		if p.clock.Sleep(ctx, wait.next()) != nil {
			return tgUser{}, false
		}
	}
}
