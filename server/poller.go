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

// pollLoop long-polls getUpdates until ctx ends. Errors back off and retry
// instead of exiting; a 409 means another getUpdates call is still open, for
// example an old process overlapping a restart.
func (b *bot) pollLoop(ctx context.Context) {
	offset, ok := b.loadOffset(ctx)
	if !ok {
		return
	}
	var wait backoff
	for ctx.Err() == nil {
		next, err := b.pollOnce(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.updateStatus(ctx, func(st *botStatus) {
				st.LastError = err.Error()
				if isUnauthorized(err) {
					st.Connected = false
				}
			})
			if b.clock.Sleep(ctx, wait.next()) != nil {
				return
			}
			continue
		}
		wait.reset()
		if next != offset {
			b.updateStatus(ctx, func(st *botStatus) { st.LastUpdateAt = b.now() })
		}
		b.updateStatus(ctx, func(st *botStatus) {
			st.LastError = ""
			st.Connected = true
		})
		offset = next
	}
}

// loadOffset reads the stored offset, retrying with backoff. Polling from 0
// instead would replay every unconfirmed update, and a replayed /new files a
// duplicate task. ok is false only when ctx ends.
func (b *bot) loadOffset(ctx context.Context) (int64, bool) {
	var wait backoff
	for retried := false; ; retried = true {
		offset, err := b.store.offset(ctx)
		if err == nil {
			if retried {
				b.updateStatus(ctx, func(st *botStatus) { st.LastError = "" })
			}
			return offset, true
		}
		if ctx.Err() != nil {
			return 0, false
		}
		log.Printf("telegram: %v", err)
		b.updateStatus(ctx, func(st *botStatus) { st.LastError = err.Error() })
		if b.clock.Sleep(ctx, wait.next()) != nil {
			return 0, false
		}
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
		next := u.UpdateID + 1
		if next <= offset {
			continue
		}
		if err := b.store.setOffset(ctx, next); err != nil {
			return offset, err
		}
		offset = next
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
	case "status":
		b.beginCommand(ctx, msg, cmd.Name)
		b.reply(ctx, msg, b.statusMessage(ctx))
		b.endCommand(ctx)
	case "new":
		if cmd.Args == "" {
			b.reply(ctx, msg, outMessage{Text: newUsage})
			return
		}
		req := triageRequest{Sender: senderName(msg.From), Text: cmd.Args}
		if msg.ReplyToMessage != nil {
			req.ReplyContext = msg.ReplyToMessage.Text
		}
		b.beginCommand(ctx, msg, cmd.Name)
		b.acknowledge(ctx, msg)
		b.reply(ctx, msg, b.fileTask(ctx, req))
		b.endCommand(ctx)
	default:
		b.reply(ctx, msg, outMessage{Text: helpText})
	}
}
