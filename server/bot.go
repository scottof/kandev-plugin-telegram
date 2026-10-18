package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// bot is the running Telegram side of the plugin: one per plugin process.
type bot struct {
	host        pluginsdk.Host
	tg          telegramClient
	cfg         config
	clock       clock
	store       stateStore
	botUsername string

	statusMu sync.Mutex
	status   botStatus

	wake           chan struct{}
	reconcileEvery time.Duration
	debounce       time.Duration
	// queueMu serialises read-modify-write of the task alert queue in state.
	queueMu sync.Mutex
}

func newBot(host pluginsdk.Host, tg telegramClient, cfg config, clk clock, botUsername string) *bot {
	return &bot{
		host:           host,
		tg:             tg,
		cfg:            cfg,
		clock:          clk,
		store:          stateStore{host: host},
		botUsername:    botUsername,
		status:         botStatus{Connected: true, Username: botUsername},
		wake:           make(chan struct{}, 1),
		reconcileEvery: reconcileInterval,
		debounce:       alertDebounce,
	}
}

// run blocks until ctx ends and both loops have stopped.
func (b *bot) run(ctx context.Context) {
	b.writeStatus(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.pollLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		b.alertLoop(ctx)
	}()
	wg.Wait()
}

// updateStatus applies change and persists the status only when it differs,
// so an idle long-poll loop does not write state every cycle.
func (b *bot) updateStatus(ctx context.Context, change func(*botStatus)) {
	b.statusMu.Lock()
	before := b.status
	change(&b.status)
	changed := b.status != before
	b.statusMu.Unlock()
	if changed {
		b.writeStatus(ctx)
	}
}

func (b *bot) writeStatus(ctx context.Context) {
	b.statusMu.Lock()
	st := b.status
	b.statusMu.Unlock()
	if err := b.store.setBotStatus(ctx, st); err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}
}

func (b *bot) now() string {
	return b.clock.Now().UTC().Format(time.RFC3339)
}

// reply answers msg in its chat. Send errors are logged, never returned: one
// failed reply must not stop the update batch.
func (b *bot) reply(ctx context.Context, msg *tgMessage, out outMessage) {
	if err := b.tg.SendMessage(ctx, msg.Chat.ID, out, msg.MessageID); err != nil && ctx.Err() == nil {
		log.Printf("telegram: replying: %v", err)
	}
}
