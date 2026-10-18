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
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-b.wake:
			timer.Stop()
			// Give a burst of events a moment to arrive so it goes out as one message.
			if b.clock.Sleep(ctx, b.debounce) != nil {
				return
			}
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

// settleQueued removes delivered alerts. Alerts queued while the send was in
// flight stay for the next cycle.
func (b *bot) settleQueued(ctx context.Context, delivered []queuedTaskAlert) error {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	queue, err := b.store.taskAlertQueue(ctx)
	if err != nil {
		return err
	}
	done := make(map[queuedTaskAlert]bool, len(delivered))
	for _, q := range delivered {
		done[q] = true
	}
	kept := queue[:0]
	for _, q := range queue {
		if !done[q] {
			kept = append(kept, q)
		}
	}
	return b.store.setTaskAlertQueue(ctx, kept)
}

// capQueue keeps the newest maxQueuedAlerts alerts.
func capQueue(queue []queuedTaskAlert) []queuedTaskAlert {
	if len(queue) > maxQueuedAlerts {
		return queue[len(queue)-maxQueuedAlerts:]
	}
	return queue
}

// flushAlerts collects new pending interactions and queued task alerts and
// sends them to every allowed user as one message. Interactions are marked
// sent after the send attempt: a crash in between repeats an alert rather
// than losing it.
func (b *bot) flushAlerts(ctx context.Context) {
	alerts, nextSent, err := b.newInteractionAlerts(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("telegram: reconciling pending interactions: %v", err)
	}
	queued, err := b.store.taskAlertQueue(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}
	for _, q := range queued {
		alerts = append(alerts, q.alert)
	}
	if len(alerts) > 0 && !b.broadcast(ctx, formatAlerts(alerts, b.cfg.KandevURL)) {
		// Nobody was reached: everything stays for the next cycle, which runs
		// on the regular interval so an outage is not retried in a tight loop.
		return
	}
	if len(queued) > 0 {
		if err := b.settleQueued(ctx, queued); err != nil && ctx.Err() == nil {
			log.Printf("telegram: %v", err)
		}
	}
	if nextSent != nil {
		if err := b.store.setSentInteractions(ctx, nextSent); err != nil && ctx.Err() == nil {
			log.Printf("telegram: %v", err)
		}
	}
}

// newInteractionAlerts diffs ListPending against the sent set. nextSent is
// the pending ids that are now alerted (resolved ids drop out), or nil when
// nothing changed or the list could not be read.
func (b *bot) newInteractionAlerts(ctx context.Context) ([]alert, map[string]bool, error) {
	pending, err := b.pendingInteractions(ctx)
	if err != nil {
		return nil, nil, err
	}
	sent, err := b.store.sentInteractions(ctx)
	if err != nil {
		return nil, nil, err
	}

	var alerts []alert
	titles := map[string]string{}
	nextSent := make(map[string]bool, len(pending))
	for _, in := range pending {
		if in.Status != "" && in.Status != pluginsdk.InteractionStatusPending {
			continue
		}
		nextSent[in.ID] = true
		if sent[in.ID] || !b.cfg.AlertInteractions {
			continue
		}
		alerts = append(alerts, interactionAlert(in, b.taskTitle(ctx, in.TaskID, titles)))
	}
	if sameSet(sent, nextSent) {
		nextSent = nil
	}
	return alerts, nextSent, nil
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

// broadcast sends msg to every allowed user and reports whether the alert is
// settled: false only when every send failed with an error worth retrying.
// Telegram rejections other than 429 (a user who never sent /start, a button
// URL it refuses) will not heal, so they settle and show in the status card.
func (b *bot) broadcast(ctx context.Context, msg outMessage) bool {
	if len(b.cfg.AllowedUserIDs) == 0 {
		return true
	}
	settled := false
	var rejected string
	for _, userID := range b.cfg.AllowedUserIDs {
		degraded, err := b.sendAlert(ctx, userID, msg)
		switch {
		case err == nil:
			settled = true
			if degraded != nil {
				rejected = fmt.Sprintf("alert to user %d sent without links: %v", userID, degraded)
			}
		case ctx.Err() != nil:
		case isPermanent(err):
			settled = true
			rejected = fmt.Sprintf("alert to user %d: %v", userID, err)
			if isForbidden(err) {
				log.Printf("telegram: user %d has not started the bot or blocked it", userID)
			} else {
				log.Printf("telegram: %s", rejected)
			}
		default:
			log.Printf("telegram: sending alert: %v", err)
		}
	}
	if settled {
		b.updateStatus(ctx, func(st *botStatus) { st.AlertError = rejected })
	}
	return settled
}

// sendAlert sends msg to one user. When Telegram rejects the message with
// 400 and it has URL buttons, the buttons are the likely cause (for example a
// loopback kandev_url), so the text goes out once more without them; degraded
// is then the original rejection. A 400 from that retry is returned as err.
func (b *bot) sendAlert(ctx context.Context, userID int64, msg outMessage) (degraded, err error) {
	err = b.tg.SendMessage(ctx, userID, msg, 0)
	if err == nil || !isBadRequest(err) || len(msg.Buttons) == 0 {
		return nil, err
	}
	if plainErr := b.tg.SendMessage(ctx, userID, outMessage{Text: msg.Text}, 0); plainErr != nil {
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
