package main

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func newMoveAlertBot(t *testing.T) (*bot, *fakeHost) {
	t.Helper()
	b, host, _ := newAlertBot(t)
	b.cfg.AlertColumnMoves = true
	host.data().steps = map[string][]pluginsdk.WorkflowStep{
		"wf-1": {{ID: "step-backlog", Name: "Backlog"}, {ID: "step-review", Name: "Review"}, {ID: "step-done", Name: "Done"}},
		"wf-2": {{ID: "step-inbox", Name: "Inbox"}},
	}
	return b, host
}

func moved(taskID, from, to, transition string) map[string]any {
	return map[string]any{
		"task_id": taskID, "from_step_id": from, "to_step_id": to,
		"from_workflow_id": "wf-1", "to_workflow_id": "wf-1", "workflow_id": "wf-1",
		"step_transition_id": transition, "move_id": "",
	}
}

func TestMoveAlert_OffByDefault(t *testing.T) {
	b, _, _ := newAlertBot(t)
	_, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-1", "step-backlog", "step-review", "tr-1"))
	require.NoError(t, err)
	require.False(t, ok)
	queue, err := b.store.taskAlertQueue(context.Background())
	require.NoError(t, err)
	require.Empty(t, queue)
}

func TestMoveAlert_NamesBothColumns(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	got, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-1", "step-backlog", "step-review", "tr-1"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, alert{Kind: alertMoved, TaskID: "task-1", TaskTitle: "Fix login", Detail: "Backlog to Review"}, got)

	msg := formatAlerts([]alert{got}, testBase)
	require.Equal(t, "Moved: Fix login\nBacklog to Review", msg.Text)
}

func TestMoveAlert_AcrossWorkflowsReadsBothWorkflows(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	payload := moved("task-1", "step-review", "step-inbox", "tr-1")
	payload["to_workflow_id"] = "wf-2"
	got, ok, err := b.moveAlertFromEvent(context.Background(), payload)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Review to Inbox", got.Detail)
}

func TestMoveAlert_RedeliveryIsDeduped(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	ctx := context.Background()
	payload := moved("task-1", "step-backlog", "step-review", "tr-1")
	_, ok, err := b.moveAlertFromEvent(ctx, payload)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = b.moveAlertFromEvent(ctx, payload)
	require.NoError(t, err)
	require.False(t, ok, "the same transition alerts once")

	_, ok, err = b.moveAlertFromEvent(ctx, moved("task-1", "step-backlog", "step-review", "tr-2"))
	require.NoError(t, err)
	require.True(t, ok, "a later move to the same column is a new alert")
}

func TestMoveAlert_CompletedTaskIsLeftToTheFinishedAlert(t *testing.T) {
	b, host := newMoveAlertBot(t)
	host.addTask(pluginsdk.Task{ID: "task-done", Title: "Ship it", State: "COMPLETED"})
	_, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-done", "step-review", "step-done", "tr-1"))
	require.NoError(t, err)
	require.False(t, ok)

	b.cfg.AlertCompleted = false
	got, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-done", "step-review", "step-done", "tr-2"))
	require.NoError(t, err)
	require.True(t, ok, "with finished alerts off, the move is the only signal")
	require.Equal(t, "Review to Done", got.Detail)
}

func TestMoveAlert_SkipsEphemeralTasksAndNoOps(t *testing.T) {
	b, host := newMoveAlertBot(t)
	host.addTask(pluginsdk.Task{ID: "task-chat", Title: "Quick chat", IsEphemeral: true})
	for _, payload := range []map[string]any{
		moved("task-chat", "step-backlog", "step-review", "tr-1"),
		moved("task-1", "step-review", "step-review", "tr-2"),
		moved("task-1", "step-review", "", "tr-3"),
	} {
		_, ok, err := b.moveAlertFromEvent(context.Background(), payload)
		require.NoError(t, err)
		require.False(t, ok)
	}
}

func TestMoveAlert_UnknownColumnsStillAlert(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	got, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-1", "step-gone", "step-new", "tr-1"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "another column to another column", got.Detail)
}

func TestOnEvent_TaskMovedQueuesTheAlert(t *testing.T) {
	b, host := newMoveAlertBot(t)
	p := newTelegramPlugin(context.Background())
	p.UnimplementedPlugin.SetHost(host)
	p.bot = b

	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventType: eventTaskMoved, Payload: moved("task-1", "step-backlog", "step-review", "tr-1")})
	require.NoError(t, err)
	queue, err := b.store.taskAlertQueue(context.Background())
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, alertMoved, queue[0].Kind)
	require.Equal(t, "Backlog to Review", queue[0].Detail, "the detail survives the state round trip")
}

func TestMoveAlert_TaskReadErrorAsksForRedelivery(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	_, ok, err := b.moveAlertFromEvent(context.Background(), moved("task-missing", "step-backlog", "step-review", "tr-1"))
	require.Error(t, err)
	require.False(t, ok)
}
