package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func permission(id, taskID, title string) pluginsdk.Interaction {
	return pluginsdk.Interaction{ID: id, Kind: pluginsdk.InteractionKindPermission, TaskID: taskID,
		Status: pluginsdk.InteractionStatusPending, Title: title, ActionType: "execute"}
}

func newAlertBot(t *testing.T, users ...int64) (*bot, *fakeHost, *fakeTelegram) {
	t.Helper()
	host := newFakeHost()
	host.addTask(pluginsdk.Task{ID: "task-1", Title: "Fix login"})
	host.addTask(pluginsdk.Task{ID: "task-2", Title: "Add cache"})
	tg := newFakeTelegram()
	if len(users) == 0 {
		users = []int64{42}
	}
	cfg := config{BotToken: testToken, AllowedUserIDs: users, KandevURL: testBase,
		AlertInteractions: true, AlertCompleted: true, AlertFailed: true, AlertReview: true}
	return newBot(host, tg, cfg, newFakeClock(), "kandev_bot"), host, tg
}

func TestFlushAlerts_NewPendingInteractionAlertsEachUserOnce(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t, 42, 43)
	host.setPending(permission("i1", "task-1", "Run npm test"))

	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 2)
	require.Equal(t, int64(42), sent[0].ChatID)
	require.Equal(t, int64(43), sent[1].ChatID)
	require.Equal(t, "Needs permission: Fix login\nRun npm test (execute)", sent[0].Msg.Text)
	require.Equal(t, testBase+"/t/task-1", sent[0].Msg.Buttons[0].URL)
	require.Equal(t, int64(0), sent[0].ReplyTo)

	b.flushAlerts(ctx)
	require.Len(t, tg.sentMessages(), 2, "a second reconcile sends nothing")
}

func TestFlushAlerts_NoDuplicateAfterRestart(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	host.setPending(permission("i1", "task-1", "Run npm test"))
	b.flushAlerts(ctx)
	require.Len(t, tg.sentMessages(), 1)

	// A new process shares only Host state with the old one.
	tg2 := newFakeTelegram()
	b2 := newBot(host, tg2, b.cfg, newFakeClock(), "kandev_bot")
	b2.flushAlerts(ctx)
	require.Empty(t, tg2.sentMessages())
}

func TestFlushAlerts_PrunesResolvedInteractions(t *testing.T) {
	ctx := context.Background()
	b, host, _ := newAlertBot(t)
	host.setPending(permission("i1", "task-1", "a"), permission("i2", "task-2", "b"))
	b.flushAlerts(ctx)

	host.setPending(permission("i2", "task-2", "b"))
	b.flushAlerts(ctx)
	sent, err := b.store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"i2": true}, sent)
}

func TestFlushAlerts_BatchesBurstIntoOneMessage(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	var items []pluginsdk.Interaction
	for i := 0; i < 5; i++ {
		items = append(items, permission(fmt.Sprintf("i%d", i), "task-1", fmt.Sprintf("step %d", i)))
	}
	host.setPending(items...)

	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.True(t, strings.HasPrefix(sent[0].Msg.Text, "5 updates from kandev:"))
}

func TestFlushAlerts_PagesThroughListPending(t *testing.T) {
	ctx := context.Background()
	b, host, _ := newAlertBot(t)
	var items []pluginsdk.Interaction
	for i := 0; i < pendingPageSize+3; i++ {
		items = append(items, permission(fmt.Sprintf("i%03d", i), "task-1", "x"))
	}
	host.setPending(items...)
	b.flushAlerts(ctx)
	sent, err := b.store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Len(t, sent, pendingPageSize+3)
}

func TestFlushAlerts_ClarificationAndUnknownTaskTitle(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	host.setPending(pluginsdk.Interaction{ID: "c1", Kind: pluginsdk.InteractionKindClarification, TaskID: "gone",
		Status:    pluginsdk.InteractionStatusPending,
		Questions: []pluginsdk.InteractionQuestion{{Prompt: "Which DB?\nmore"}}})
	b.flushAlerts(ctx)
	require.Equal(t, "Has a question: Untitled task\nWhich DB?", tg.sentMessages()[0].Msg.Text)
}

func TestFlushAlerts_InteractionToggleOffMarksSeenWithoutSending(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	b.cfg.AlertInteractions = false
	host.setPending(permission("i1", "task-1", "x"))
	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages())

	// Turning the toggle on later does not replay old prompts.
	b.cfg.AlertInteractions = true
	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages())
}

func TestFlushAlerts_ListPendingErrorStillSendsQueuedTaskAlerts(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	host.data().listPendingErr = errors.New("unavailable")
	b.enqueue(alert{Kind: alertCompleted, TaskID: "task-1", TaskTitle: "Fix login"})
	b.flushAlerts(ctx)
	require.Len(t, tg.sentMessages(), 1)
	require.Equal(t, "Finished: Fix login", tg.sentMessages()[0].Msg.Text)
}

func TestFlushAlerts_ForbiddenUserDoesNotBlockOthers(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t, 42, 43)
	tg.sendErrTo = map[int64]error{42: &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden: bot can't initiate conversation with a user"}}
	host.setPending(permission("i1", "task-1", "x"))
	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Equal(t, int64(43), sent[0].ChatID)

	b.flushAlerts(ctx)
	require.Len(t, tg.sentMessages(), 1, "the interaction is marked sent even if one user could not be reached")
}

func TestFlushAlerts_NoAllowedUsersSendsNothing(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	b.cfg.AllowedUserIDs = nil
	host.setPending(permission("i1", "task-1", "x"))
	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages())
}

func stateChanged(taskID, oldState, newState, updatedAt string) map[string]any {
	return map[string]any{
		"task_id": taskID, "title": "Fix login", "state": newState,
		"old_state": oldState, "new_state": newState, "updated_at": updatedAt, "is_ephemeral": false,
	}
}

func TestTaskAlertFromEvent_Mapping(t *testing.T) {
	cases := []struct {
		oldState, newState string
		want               alertKind
		ok                 bool
	}{
		{"IN_PROGRESS", "COMPLETED", alertCompleted, true},
		{"SCHEDULING", "FAILED", alertFailed, true},
		{"IN_PROGRESS", "REVIEW", alertReview, true},
		{"REVIEW", "REVIEW", "", false},
		{"TODO", "IN_PROGRESS", "", false},
		{"REVIEW", "CANCELLED", "", false},
		{"IN_PROGRESS", "WAITING_FOR_INPUT", "", false},
	}
	for i, tc := range cases {
		b, _, _ := newAlertBot(t)
		got, ok, err := b.taskAlertFromEvent(context.Background(), stateChanged("task-1", tc.oldState, tc.newState, fmt.Sprintf("t%d", i)))
		require.NoError(t, err)
		require.Equal(t, tc.ok, ok, "%s -> %s", tc.oldState, tc.newState)
		if ok {
			require.Equal(t, alert{Kind: tc.want, TaskID: "task-1", TaskTitle: "Fix login"}, got)
		}
	}
}

func TestTaskAlertFromEvent_DedupesRedeliveryAcrossRestart(t *testing.T) {
	ctx := context.Background()
	b, host, _ := newAlertBot(t)
	payload := stateChanged("task-1", "IN_PROGRESS", "COMPLETED", "2026-10-18T10:00:00Z")
	_, ok, err := b.taskAlertFromEvent(ctx, payload)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = b.taskAlertFromEvent(ctx, payload)
	require.NoError(t, err)
	require.False(t, ok, "a redelivered event is skipped")

	b2 := newBot(host, newFakeTelegram(), b.cfg, newFakeClock(), "kandev_bot")
	_, ok, err = b2.taskAlertFromEvent(ctx, payload)
	require.NoError(t, err)
	require.False(t, ok, "the dedupe log survives a restart")

	_, ok, err = b2.taskAlertFromEvent(ctx, stateChanged("task-1", "COMPLETED", "REVIEW", "2026-10-18T11:00:00Z"))
	require.NoError(t, err)
	require.True(t, ok, "a later transition of the same task alerts")
}

func TestTaskAlertFromEvent_TogglesAndEphemeral(t *testing.T) {
	ctx := context.Background()
	b, _, _ := newAlertBot(t)
	b.cfg.AlertCompleted, b.cfg.AlertFailed, b.cfg.AlertReview = false, false, false
	for _, s := range []string{"COMPLETED", "FAILED", "REVIEW"} {
		_, ok, err := b.taskAlertFromEvent(ctx, stateChanged("task-1", "IN_PROGRESS", s, s))
		require.NoError(t, err)
		require.False(t, ok, s)
	}

	b, _, _ = newAlertBot(t)
	quickChat := stateChanged("task-1", "IN_PROGRESS", "REVIEW", "x")
	quickChat["is_ephemeral"] = true
	_, ok, err := b.taskAlertFromEvent(ctx, quickChat)
	require.NoError(t, err)
	require.False(t, ok, "quick chat tasks never alert")
}

func TestTaskAlertFromEvent_IgnoresMalformedPayload(t *testing.T) {
	b, _, _ := newAlertBot(t)
	_, ok, err := b.taskAlertFromEvent(context.Background(), map[string]any{"new_state": "COMPLETED"})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestOnEvent_TaskAlertIsQueuedAndFlushedWithInteractions(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	p := &telegramPlugin{bot: b}
	require.NoError(t, p.OnEvent(ctx, &pluginsdk.Event{EventType: "task.state_changed",
		Payload: stateChanged("task-2", "IN_PROGRESS", "COMPLETED", "x")}))
	host.setPending(permission("i1", "task-1", "Run npm test"))

	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Contains(t, sent[0].Msg.Text, "2 updates from kandev:")
	require.Len(t, sent[0].Msg.Buttons, 2)
}

func TestOnEvent_PendingActionChangedWakesAlertLoop(t *testing.T) {
	b, _, _ := newAlertBot(t)
	p := &telegramPlugin{bot: b}
	require.NoError(t, p.OnEvent(context.Background(), &pluginsdk.Event{EventType: "session.pending_action_changed",
		Payload: map[string]any{"pending_action": "permission"}}))
	select {
	case <-b.wake:
	default:
		t.Fatal("expected a wake signal")
	}
}

func TestAlertLoop_ReconcilesOnWakeAndStopsOnCancel(t *testing.T) {
	b, host, tg := newAlertBot(t)
	b.reconcileEvery = time.Hour
	b.debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.alertLoop(ctx)
		close(done)
	}()

	host.setPending(permission("i1", "task-1", "x"))
	b.nudge()
	require.Eventually(t, func() bool { return len(tg.sentMessages()) == 1 }, 2*time.Second, 5*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("alert loop did not stop")
	}
}

func TestFlushAlerts_TransientSendFailureRetriesNextCycle(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	tg.sendErr = errors.New("telegram sendMessage: request failed: connection reset")
	host.setPending(permission("i1", "task-1", "x"))
	b.enqueue(alert{Kind: alertCompleted, TaskID: "task-2", TaskTitle: "Add cache"})

	b.flushAlerts(ctx)
	sent, err := b.store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Empty(t, sent, "nothing is marked sent when no user was reached")

	tg.sendErr = nil
	b.flushAlerts(ctx)
	msgs := tg.sentMessages()
	require.Len(t, msgs, 1)
	require.Contains(t, msgs[0].Msg.Text, "2 updates from kandev:", "the interaction and the requeued task alert both go out")
}

func TestFlushAlerts_AllUsersForbiddenStillMarksSent(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newAlertBot(t)
	tg.sendErr = &telegramAPIError{Method: "sendMessage", Code: 403, Description: "Forbidden"}
	host.setPending(permission("i1", "task-1", "x"))
	b.flushAlerts(ctx)
	sent, err := b.store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"i1": true}, sent, "a 403 will not heal by retrying")
}
