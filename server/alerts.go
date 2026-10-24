package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	eventTaskStateChanged     = "task.state_changed"
	eventPendingActionChanged = "session.pending_action_changed"

	reconcileInterval = 15 * time.Second
	alertDebounce     = 2 * time.Second
	pendingPageSize   = 100
	maxPendingPages   = 20
	// maxQueuedAlerts bounds task alerts waiting for delivery, so a long
	// Telegram outage cannot grow the queue without limit.
	maxQueuedAlerts = 50
	untitledTask    = "Untitled task"
)

// alertLoop sends alerts until ctx ends. Pending interactions come from the
// durable ListPending record on a timer, because the plugin may start after a
// prompt was raised or miss its event; events only make it check sooner.
func (b *bot) alertLoop(ctx context.Context) {
	for {
		b.flushAlerts(ctx)
		timer := time.NewTimer(b.reconcileEvery)
		ok := b.waitForFlush(ctx, timer.C)
		timer.Stop()
		if !ok {
			return
		}
	}
}

// waitForFlush blocks until the next flush is due: the reconcile tick, or a
// wake. After a flush that left a retryable failure, wakes are ignored until
// retryAt, so events arriving during an outage do not turn the 15 second
// retry into one every debounce. ok is false when ctx ends.
func (b *bot) waitForFlush(ctx context.Context, tick <-chan time.Time) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick:
			return true
		case <-b.wake:
			if b.clock.Now().Before(b.retryAt) {
				continue
			}
			// Give a burst of events a moment to arrive so it goes out as one message.
			return b.clock.Sleep(ctx, b.debounce) == nil
		}
	}
}

// nudge asks the alert loop to reconcile now. It never blocks.
func (b *bot) nudge() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// enqueue stores a task alert in Host state until a send settles it, so a
// restart during the debounce or a Telegram outage cannot lose it.
func (b *bot) enqueue(ctx context.Context, qa queuedTaskAlert) error {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	queue, err := b.store.taskAlertQueue(ctx)
	if err != nil {
		return err
	}
	for _, q := range queue {
		if q.TaskID == qa.TaskID && q.Marker == qa.Marker {
			return nil
		}
	}
	return b.store.setTaskAlertQueue(ctx, capQueue(append(queue, qa)))
}

// markSettled records in memory that a queued task alert is settled for a
// user, so a failed queue write cannot make the next cycle send it again.
func (b *bot) markSettled(k taskAlertKey, userID int64) {
	if b.settledMemo == nil {
		b.settledMemo = map[taskAlertKey]map[int64]bool{}
	}
	if b.settledMemo[k] == nil {
		b.settledMemo[k] = map[int64]bool{}
	}
	b.settledMemo[k][userID] = true
}

func (b *bot) isSettled(q queuedTaskAlert, userID int64) bool {
	return q.settledFor(userID) || b.settledMemo[q.key()][userID]
}

// settleQueued writes the in-memory settled users into the stored queue and
// drops alerts settled for every allowed user. Alerts queued while the send
// was in flight stay for the next cycle. Once the write lands, the memory
// copy is only kept for alerts still queued.
func (b *bot) settleQueued(ctx context.Context) error {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	queue, err := b.store.taskAlertQueue(ctx)
	if err != nil {
		return err
	}
	kept := queue[:0]
	for _, q := range queue {
		for userID := range b.settledMemo[q.key()] {
			if !q.settledFor(userID) {
				q.SettledFor = append(q.SettledFor, userID)
			}
		}
		if !b.settledForAll(q) {
			kept = append(kept, q)
		}
	}
	if err := b.store.setTaskAlertQueue(ctx, kept); err != nil {
		return err
	}
	stillQueued := make(map[taskAlertKey]bool, len(kept))
	for _, q := range kept {
		stillQueued[q.key()] = true
	}
	for k := range b.settledMemo {
		if !stillQueued[k] {
			delete(b.settledMemo, k)
		}
	}
	return nil
}

func (b *bot) settledForAll(q queuedTaskAlert) bool {
	for _, userID := range b.cfg.AllowedUserIDs {
		if !q.settledFor(userID) {
			return false
		}
	}
	return true
}

// capQueue keeps the newest maxQueuedAlerts alerts.
func capQueue(queue []queuedTaskAlert) []queuedTaskAlert {
	if len(queue) > maxQueuedAlerts {
		return queue[len(queue)-maxQueuedAlerts:]
	}
	return queue
}

// flushAlerts sends each allowed user one message with the pending
// interactions and queued task alerts not yet settled for that user. A user is
// settled on delivery or on a permanent Telegram rejection; a retryable
// failure for one user leaves the others untouched. Interactions are marked
// sent after the send attempt: a crash in between repeats an alert rather
// than losing it. A retryable failure for any user sets retryAt one reconcile
// interval ahead.
func (b *bot) flushAlerts(ctx context.Context) {
	retry := false
	pending, stored, err := b.interactionState(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("telegram: reconciling pending interactions: %v", err)
	}
	queued, err := b.store.taskAlertQueue(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}

	titles := map[string]string{}
	nextSent := sentSet{}
	anySettled := false
	var rejected string
	for _, userID := range b.cfg.AllowedUserIDs {
		var alerts []alert
		var newIDs []string
		for _, in := range pending {
			if stored.has(userID, in.ID) || b.sentMemo.has(userID, in.ID) || !b.cfg.AlertInteractions {
				nextSent.add(userID, in.ID)
				continue
			}
			alerts = append(alerts, interactionAlert(in, b.taskTitle(ctx, in.TaskID, titles)))
			newIDs = append(newIDs, in.ID)
		}
		var keys []taskAlertKey
		for _, q := range queued {
			if !b.isSettled(q, userID) {
				alerts = append(alerts, q.alert)
				keys = append(keys, q.key())
			}
		}
		if len(alerts) == 0 {
			continue
		}

		settled, rejection := b.deliver(ctx, userID, formatAlerts(alerts, b.cfg.KandevURL))
		if !settled {
			retry = true
			continue
		}
		anySettled = true
		if rejection != "" {
			rejected = rejection
		}
		for _, id := range newIDs {
			nextSent.add(userID, id)
		}
		for _, k := range keys {
			b.markSettled(k, userID)
		}
	}
	if anySettled {
		b.updateStatus(ctx, func(st *botStatus) { st.AlertError = rejected })
	}
	if retry {
		b.retryAt = b.clock.Now().Add(b.reconcileEvery)
	}

	if len(queued) > 0 {
		if err := b.settleQueued(ctx); err != nil && ctx.Err() == nil {
			log.Printf("telegram: %v", err)
		}
	}
	if stored != nil {
		b.sentMemo = nextSent
		if !nextSent.equal(stored) {
			if err := b.store.setSentInteractions(ctx, nextSent); err != nil && ctx.Err() == nil {
				log.Printf("telegram: %v", err)
			}
		}
	}
}

// interactionState returns the pending interactions and the stored sent set.
// The in-memory sentMemo is consulted alongside it, so a failing state write
// does not repeat alerts every cycle. Both are nil when either read fails;
// interactions are then skipped this cycle and the sent set is left alone.
func (b *bot) interactionState(ctx context.Context) ([]pluginsdk.Interaction, sentSet, error) {
	all, err := b.pendingInteractions(ctx)
	if err != nil {
		return nil, nil, err
	}
	stored, err := b.store.sentInteractions(ctx)
	if err != nil {
		return nil, nil, err
	}
	pending := all[:0:0]
	for _, in := range all {
		if in.Status == "" || in.Status == pluginsdk.InteractionStatusPending {
			pending = append(pending, in)
		}
	}
	return pending, stored, nil
}

func listAllPending(ctx context.Context, accessor pluginsdk.InteractionAccessor) ([]pluginsdk.Interaction, error) {
	var all []pluginsdk.Interaction
	page := pluginsdk.Page{Limit: pendingPageSize}
	for i := 0; i < maxPendingPages; i++ {
		items, info, err := accessor.ListPending(ctx, pluginsdk.InteractionFilter{}, page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if info == nil || !info.HasMore || info.NextCursor == "" {
			break
		}
		page.Cursor = info.NextCursor
	}
	return all, nil
}

// taskTitle looks a title up once per flush; a failed lookup still alerts.
func (b *bot) taskTitle(ctx context.Context, taskID string, cache map[string]string) string {
	if title, ok := cache[taskID]; ok {
		return title
	}
	title := untitledTask
	if task, err := b.host.Tasks().Get(ctx, taskID); err == nil && task != nil && task.Title != "" {
		title = task.Title
	}
	cache[taskID] = title
	return title
}

// deliver sends msg to one user and reports whether it is settled for them:
// delivered, or rejected for good. Telegram rejections other than 429 (a user
// who never sent /start, a button URL it refuses) will not heal, so they
// settle; rejection then describes them for the status card.
func (b *bot) deliver(ctx context.Context, userID int64, msg outMessage) (settled bool, rejection string) {
	degraded, err := b.sendAlert(ctx, userID, msg)
	switch {
	case err == nil:
		if degraded != nil {
			rejection = fmt.Sprintf("alert to user %d sent without links: %v", userID, degraded)
		}
		return true, rejection
	case ctx.Err() != nil:
		return false, ""
	case isPermanent(err):
		rejection = fmt.Sprintf("alert to user %d: %v", userID, err)
		if isForbidden(err) {
			log.Printf("telegram: user %d has not started the bot or blocked it", userID)
		} else {
			log.Printf("telegram: %s", rejection)
		}
		return true, rejection
	default:
		log.Printf("telegram: sending alert: %v", err)
		return false, ""
	}
}

// sendAlert sends msg to one user, falling back to plain text as
// sendWithFallback describes.
func (b *bot) sendAlert(ctx context.Context, userID int64, msg outMessage) (degraded, err error) {
	return b.sendWithFallback(ctx, userID, msg, 0)
}

// sendWithFallback sends msg to chatID. When Telegram rejects the message with
// 400 and it has URL buttons, the buttons are the likely cause (for example a
// loopback kandev_url), so the text goes out once more without them; degraded
// is then the original rejection. A 400 from that retry is returned as err.
func (b *bot) sendWithFallback(ctx context.Context, chatID int64, msg outMessage, replyTo int64) (degraded, err error) {
	err = b.tg.SendMessage(ctx, chatID, msg, replyTo)
	if err == nil || !isBadRequest(err) || len(msg.Buttons) == 0 {
		return nil, err
	}
	if plainErr := b.tg.SendMessage(ctx, chatID, outMessage{Text: msg.Text}, replyTo); plainErr != nil {
		return nil, plainErr
	}
	return err, nil
}

// taskAlertFromEvent maps a task.state_changed payload to an alert and queues
// it in Host state; ok reports a new alert worth waking the loop for. Every
// finished agent turn moves a task to REVIEW, so that alert is opt-in. An
// agent crash also lands in REVIEW; FAILED only covers start failures.
func (b *bot) taskAlertFromEvent(ctx context.Context, payload map[string]any) (alert, bool, error) {
	taskID, _ := payload["task_id"].(string)
	newState, _ := payload["new_state"].(string)
	oldState, _ := payload["old_state"].(string)
	if taskID == "" || newState == "" || newState == oldState {
		return alert{}, false, nil
	}
	if ephemeral, _ := payload["is_ephemeral"].(bool); ephemeral {
		return alert{}, false, nil
	}

	var kind alertKind
	switch {
	case newState == "COMPLETED" && b.cfg.AlertCompleted:
		kind = alertCompleted
	case newState == "FAILED" && b.cfg.AlertFailed:
		kind = alertFailed
	case newState == "REVIEW" && b.cfg.AlertReview:
		kind = alertReview
	default:
		return alert{}, false, nil
	}

	updatedAt, _ := payload["updated_at"].(string)
	marker := newState + "@" + updatedAt
	alertLog, err := b.store.taskAlerts(ctx)
	if err != nil {
		return alert{}, false, err
	}
	if alertLog.seen(taskID, marker) {
		return alert{}, false, nil
	}

	title, _ := payload["title"].(string)
	if title == "" {
		title = untitledTask
	}
	a := alert{Kind: kind, TaskID: taskID, TaskTitle: title}
	// Queue before marking seen: if the marker write fails, the redelivered
	// event finds the alert already queued instead of losing it.
	if err := b.enqueue(ctx, queuedTaskAlert{alert: a, Marker: marker}); err != nil {
		return alert{}, false, err
	}
	if err := b.store.setTaskAlerts(ctx, alertLog.add(taskID, marker)); err != nil {
		return alert{}, false, err
	}
	return a, true, nil
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
