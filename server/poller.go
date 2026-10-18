package main

import (
	"context"
	"log"
	"time"
)

const (
	pollTimeoutSeconds = 50
	backoffMin         = 5 * time.Second
	backoffMax         = 60 * time.Second
)

// backoff doubles from backoffMin up to backoffMax.
type backoff struct {
	current time.Duration
}

func (b *backoff) next() time.Duration {
	if b.current == 0 {
		b.current = backoffMin
	} else {
		b.current *= 2
		if b.current > backoffMax {
			b.current = backoffMax
		}
	}
	return b.current
}

func (b *backoff) reset() { b.current = 0 }

// pollLoop long-polls getUpdates until ctx ends. A 409 means another
// getUpdates call is still open (an old process overlapping a restart), so the
// loop waits and retries instead of exiting.
func (b *bot) pollLoop(ctx context.Context) {
	offset, err := b.store.offset(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v; starting from the latest update", err)
	}
	var wait backoff
	for ctx.Err() == nil {
		next, err := b.pollOnce(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.updateStatus(ctx, func(st *botStatus) { st.LastError = err.Error() })
			if b.clock.Sleep(ctx, wait.next()) != nil {
				return
			}
			continue
		}
		wait.reset()
		if next != offset {
			b.updateStatus(ctx, func(st *botStatus) { st.LastUpdateAt = b.now() })
		}
		b.updateStatus(ctx, func(st *botStatus) { st.LastError = "" })
		offset = next
	}
}

// pollOnce fetches one batch and handles it. The offset is persisted before
// each update is handled: a crash mid-command loses that command rather than
// replaying it (a replayed /new would file a duplicate task).
func (b *bot) pollOnce(ctx context.Context, offset int64) (int64, error) {
	updates, err := b.tg.GetUpdates(ctx, offset, pollTimeoutSeconds)
	if err != nil {
		return offset, err
	}
	for _, u := range updates {
		if u.UpdateID+1 > offset {
			offset = u.UpdateID + 1
		}
		if err := b.store.setOffset(ctx, offset); err != nil {
			log.Printf("telegram: %v", err)
		}
		b.handleUpdate(ctx, u)
	}
	return offset, nil
}

// handleUpdate drops anything that is not a private message from an allowed
// user, without replying, then dispatches commands.
func (b *bot) handleUpdate(ctx context.Context, u tgUpdate) {
	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat.Type != "private" || !b.cfg.isAllowed(msg.From.ID) {
		return
	}
	cmd, ok := parseCommand(msg.Text, b.botUsername)
	if !ok {
		if msg.Text != "" && msg.Text[0] == '/' {
			return // addressed to another bot
		}
		b.reply(ctx, msg, outMessage{Text: helpText})
		return
	}
	switch cmd.Name {
	default:
		b.reply(ctx, msg, outMessage{Text: helpText})
	}
}
