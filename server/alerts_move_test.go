package main

import (
	"context"
	"fmt"
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

func taskEvent(taskID, step, state, updatedAt string) map[string]any {
	return map[string]any{
		"task_id": taskID, "workflow_step_id": step, "workflow_id": "wf-1", "title": "Fix login",
		"state": state, "updated_at": updatedAt, "step_transition_id": "", "is_ephemeral": false,
	}
}

func columnEvent(t *testing.T, b *bot, eventType string, payload map[string]any) (alert, bool) {
	t.Helper()
	a, ok, err := b.columnAlertFromEvent(context.Background(), eventType, payload)
	require.NoError(t, err)
	return a, ok
}

func TestColumnAlert_OffByDefault(t *testing.T) {
	b, _, _ := newAlertBot(t)
	_, ok := columnEvent(t, b, eventTaskMoved, moved("task-1", "step-backlog", "step-review", "tr-1"))
	require.False(t, ok)
	steps, err := b.store.taskSteps(context.Background())
	require.NoError(t, err)
	require.Empty(t, steps, "nothing is recorded while the setting is off")
}

func TestColumnAlert_BoardMoveNamesBothColumns(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	got, ok := columnEvent(t, b, eventTaskMoved, moved("task-1", "step-backlog", "step-review", "tr-1"))
	require.True(t, ok)
	require.Equal(t, alert{Kind: alertMoved, TaskID: "task-1", TaskTitle: "Fix login", Detail: "Backlog to Review", WorkflowID: "wf-1"}, got)
	require.Equal(t, "Moved: Fix login\nBacklog to Review", formatAlerts([]alert{got}, testBase).Text)
}

func TestColumnAlert_AutomaticMoveFromTaskUpdated(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	_, ok := columnEvent(t, b, eventTaskCreated, taskEvent("task-1", "step-backlog", "IN_PROGRESS", "t1"))
	require.False(t, ok, "creation only records the column")

	got, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-review", "REVIEW", "t2"))
	require.True(t, ok, "a workflow move only publishes task.updated")
	require.Equal(t, "Backlog to Review", got.Detail)
}

func TestColumnAlert_OneMoveAlertsOnceWhicheverEventComesFirst(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	columnEvent(t, b, eventTaskCreated, taskEvent("task-1", "step-backlog", "TODO", "t1"))

	_, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-review", "TODO", "t2"))
	require.True(t, ok)
	_, ok = columnEvent(t, b, eventTaskMoved, moved("task-1", "step-backlog", "step-review", "tr-1"))
	require.False(t, ok, "task.moved for the same move finds the column recorded")

	_, ok = columnEvent(t, b, eventTaskMoved, moved("task-1", "step-review", "step-backlog", "tr-2"))
	require.True(t, ok)
	_, ok = columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-backlog", "TODO", "t3"))
	require.False(t, ok, "and task.updated after task.moved is quiet too")
}

func TestColumnAlert_FirstSeenThroughUpdateIsOnlyRecorded(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	_, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-review", "TODO", "t1"))
	require.False(t, ok, "the earlier column is unknown")
	_, ok = columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-done", "TODO", "t2"))
	require.True(t, ok)
}

func TestColumnAlert_OtherUpdatesAreQuiet(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	columnEvent(t, b, eventTaskCreated, taskEvent("task-1", "step-backlog", "TODO", "t1"))
	renamed := taskEvent("task-1", "step-backlog", "TODO", "t2")
	renamed["title"] = "Fix the login page"
	_, ok := columnEvent(t, b, eventTaskUpdated, renamed)
	require.False(t, ok)
}

func TestColumnAlert_RepeatedMoveWithoutTransitionIDStillAlerts(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	columnEvent(t, b, eventTaskCreated, taskEvent("task-1", "step-backlog", "TODO", "t0"))
	for i, step := range []string{"step-review", "step-backlog", "step-review"} {
		_, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", step, "TODO", fmt.Sprintf("t%d", i+1)))
		require.True(t, ok, "move %d", i+1)
	}
}

func TestColumnAlert_RedeliveredMoveIsDeduped(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	payload := moved("task-1", "step-backlog", "step-review", "tr-1")
	_, ok := columnEvent(t, b, eventTaskMoved, payload)
	require.True(t, ok)
	_, ok = columnEvent(t, b, eventTaskMoved, payload)
	require.False(t, ok)
}

func TestColumnAlert_CompletedTaskIsLeftToTheFinishedAlert(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	columnEvent(t, b, eventTaskCreated, taskEvent("task-1", "step-review", "REVIEW", "t1"))
	_, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-done", "COMPLETED", "t2"))
	require.False(t, ok)

	b.cfg.AlertCompleted = false
	got, ok := columnEvent(t, b, eventTaskUpdated, taskEvent("task-1", "step-review", "COMPLETED", "t3"))
	require.True(t, ok, "with finished alerts off, the move is the only signal")
	require.Equal(t, "Done to Review", got.Detail)
}

func TestColumnAlert_MovedEventReadsTheTask(t *testing.T) {
	b, host := newMoveAlertBot(t)
	host.addTask(pluginsdk.Task{ID: "task-done", Title: "Ship it", State: "COMPLETED"})
	_, ok := columnEvent(t, b, eventTaskMoved, moved("task-done", "step-review", "step-done", "tr-1"))
	require.False(t, ok, "the task read shows the move completed it")

	host.addTask(pluginsdk.Task{ID: "task-chat", Title: "Quick chat", IsEphemeral: true})
	_, ok = columnEvent(t, b, eventTaskMoved, moved("task-chat", "step-backlog", "step-review", "tr-2"))
	require.False(t, ok)

	_, _, err := b.columnAlertFromEvent(context.Background(), eventTaskMoved, moved("task-missing", "step-backlog", "step-review", "tr-3"))
	require.Error(t, err, "a failed task read asks for redelivery")
}

func TestColumnAlert_EphemeralUpdatesAndUnknownColumns(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	chat := taskEvent("task-chat", "step-review", "TODO", "t1")
	chat["is_ephemeral"] = true
	_, ok := columnEvent(t, b, eventTaskMoved, moved("task-1", "step-gone", "step-new", "tr-1"))
	require.True(t, ok)
	steps, err := b.store.taskSteps(context.Background())
	require.NoError(t, err)
	require.Equal(t, "step-new", steps.get("task-1"))

	_, ok = columnEvent(t, b, eventTaskUpdated, chat)
	require.False(t, ok)
	steps, err = b.store.taskSteps(context.Background())
	require.NoError(t, err)
	require.Empty(t, steps.get("task-chat"), "quick chat tasks are not recorded")

	got, ok := columnEvent(t, b, eventTaskMoved, moved("task-2", "step-gone", "step-new", "tr-2"))
	require.True(t, ok)
	require.Equal(t, "another column to another column", got.Detail)
}

func TestColumnAlert_AcrossWorkflowsReadsBothWorkflows(t *testing.T) {
	b, _ := newMoveAlertBot(t)
	payload := moved("task-1", "step-review", "step-inbox", "tr-1")
	payload["to_workflow_id"] = "wf-2"
	got, ok := columnEvent(t, b, eventTaskMoved, payload)
	require.True(t, ok)
	require.Equal(t, "Review to Inbox", got.Detail)
}

func TestTaskStepLog_CapsOldestEntries(t *testing.T) {
	var steps taskStepLog
	for i := 0; i < taskStepsCap+5; i++ {
		steps = steps.set(fmt.Sprintf("task-%d", i), "step")
	}
	require.Len(t, steps, taskStepsCap)
	require.Empty(t, steps.get("task-0"))
	steps = steps.set("task-10", "other")
	require.Equal(t, "other", steps.get("task-10"))
	require.Equal(t, "task-10", steps[len(steps)-1].TaskID, "a recorded task moves to the newest end")
}

func TestOnEvent_TaskUpdatedQueuesTheColumnAlert(t *testing.T) {
	b, host := newMoveAlertBot(t)
	p := newTelegramPlugin(context.Background())
	p.UnimplementedPlugin.SetHost(host)
	p.bot = b

	require.NoError(t, p.OnEvent(context.Background(), &pluginsdk.Event{EventType: eventTaskCreated, Payload: taskEvent("task-1", "step-backlog", "TODO", "t1")}))
	require.NoError(t, p.OnEvent(context.Background(), &pluginsdk.Event{EventType: eventTaskUpdated, Payload: taskEvent("task-1", "step-review", "REVIEW", "t2")}))
	queue, err := b.store.taskAlertQueue(context.Background())
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, alertMoved, queue[0].Kind)
	require.Equal(t, "Backlog to Review", queue[0].Detail, "the detail survives the state round trip")
}
