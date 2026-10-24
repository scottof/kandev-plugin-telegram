package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

// newActionBot has task-1 "Fix login" in wf-1 / Backlog, with a Done column.
func newActionBot(t *testing.T) (*bot, *fakeHost, *fakeTelegram) {
	t.Helper()
	b, host, tg := newAlertBot(t)
	d := host.data()
	d.workspaces = []pluginsdk.Workspace{{ID: "ws-1", Name: "Personal"}}
	d.workflows = []pluginsdk.Workflow{{ID: "wf-1", WorkspaceID: "ws-1", Name: "Solo Dev"}, {ID: "wf-2", WorkspaceID: "ws-1", Name: "Development"}}
	d.steps = map[string][]pluginsdk.WorkflowStep{
		"wf-1": {{ID: "step-backlog", Name: "Backlog", Position: 1}, {ID: "step-done", Name: "Done", Position: 3}, {ID: "step-review", Name: "Review", Position: 2}},
		"wf-2": {{ID: "step-todo", Name: "To do", Position: 1}, {ID: "step-shipped-later", Name: "Later", Position: 2}},
	}
	d.mu.Lock()
	d.tasks = []pluginsdk.Task{
		{ID: "task-1", Title: "Fix login", WorkspaceID: "ws-1", WorkflowID: "wf-1", WorkflowStepID: "step-backlog", State: "TODO", UpdatedAt: "2026-10-24T10:00:00Z"},
		{ID: "task-2", Title: "Add login cache", WorkspaceID: "ws-1", WorkflowID: "wf-2", WorkflowStepID: "step-todo", State: "IN_PROGRESS", UpdatedAt: "2026-10-24T09:00:00Z"},
	}
	d.mu.Unlock()
	return b, host, tg
}

func press(t *testing.T, b *bot, tg *fakeTelegram, from int64, data string) string {
	t.Helper()
	before := len(tg.sentAnswers())
	b.handleUpdate(context.Background(), tgUpdate{UpdateID: 1, CallbackQuery: &tgCallbackQuery{ID: "cb-1", From: tgUser{ID: from}, Data: data}})
	answers := tg.sentAnswers()
	if len(answers) == before {
		return ""
	}
	return answers[len(answers)-1].Text
}

func TestTaskActions(t *testing.T) {
	require.Equal(t, []linkButton{{Text: "Mute", Data: "m:task-1"}, {Text: "Done", Data: "d:task-1"}}, taskActions("task-1", alertPermission))
	require.Equal(t, []linkButton{{Text: "Mute", Data: "m:task-1"}}, taskActions("task-1", alertCompleted))
	data := taskActions("0c438666-097e-46d5-80f0-28b75d1ca31e", alertFailed)[1].Data
	require.LessOrEqual(t, len(data), 64, "Telegram callback data is at most 64 bytes")
}

func TestCallback_MuteButton(t *testing.T) {
	b, _, tg := newActionBot(t)
	require.Equal(t, "Muted Fix login. Send /unmute to undo.", press(t, b, tg, 42, "m:task-1"))
	muted, err := b.store.mutedTasks(context.Background())
	require.NoError(t, err)
	require.Equal(t, mutedList{"task-1"}, muted)
	require.Equal(t, "Fix login is already muted. Send /unmute to undo.", press(t, b, tg, 42, "m:task-1"))
}

func TestCallback_DoneMovesToTheFinishingColumn(t *testing.T) {
	b, host, tg := newActionBot(t)
	require.Equal(t, "Moved Fix login to Done.", press(t, b, tg, 42, "d:task-1"))
	require.Equal(t, []pluginsdk.MoveTaskInput{{TaskID: "task-1", WorkflowStepID: "step-done"}}, host.data().moves)
}

func TestCallback_DoneWithoutAFinishingNameUsesTheLastColumn(t *testing.T) {
	b, host, tg := newActionBot(t)
	require.Equal(t, "Moved Add login cache to Later.", press(t, b, tg, 42, "d:task-2"))
	require.Equal(t, "step-shipped-later", host.data().moves[0].WorkflowStepID)
}

func TestCallback_DoneEdgeCases(t *testing.T) {
	b, host, tg := newActionBot(t)
	host.addTask(pluginsdk.Task{ID: "task-3", Title: "Ship it", WorkflowID: "wf-1", WorkflowStepID: "step-done"})
	require.Equal(t, "Ship it is already in Done.", press(t, b, tg, 42, "d:task-3"))

	host.data().moveErr = errors.New("permission denied")
	require.Equal(t, "Could not move it: permission denied", press(t, b, tg, 42, "d:task-1"))

	require.Equal(t, "That task no longer exists.", press(t, b, tg, 42, "d:task-missing"))
	require.Equal(t, "This button no longer works.", press(t, b, tg, 42, "garbage"))
	require.Equal(t, "This button no longer works.", press(t, b, tg, 42, "x:task-1"))
}

func TestCallback_StrangersAreIgnored(t *testing.T) {
	b, host, tg := newActionBot(t)
	require.Empty(t, press(t, b, tg, 999, "d:task-1"))
	require.Empty(t, host.data().moves)
	require.Empty(t, tg.sentAnswers())
}

func TestTaskCommand(t *testing.T) {
	b, _, _ := newActionBot(t)
	ctx := context.Background()

	one := b.taskCommand(ctx, "FIX")
	require.Equal(t, "Fix login\nWorkflow: Solo Dev\nColumn: Backlog\nState: TODO\nUpdated: 2026-10-24 10:00 UTC", one.Text)
	require.Equal(t, []linkButton{
		{Text: "Open task", URL: testBase + "/t/task-1"},
		{Text: "Mute", Data: "m:task-1"},
		{Text: "Done", Data: "d:task-1"},
	}, one.Buttons)

	many := b.taskCommand(ctx, "login")
	require.Contains(t, many.Text, "Several tasks match")
	require.Contains(t, many.Text, "- Fix login (TODO)")
	require.Contains(t, many.Text, "- Add login cache (IN_PROGRESS)")

	require.Equal(t, noTaskMatch, b.taskCommand(ctx, "payroll").Text)
	require.Equal(t, taskUsage, b.taskCommand(ctx, "  ").Text)
	require.Equal(t, noTaskMatch, b.taskCommand(ctx, "login fix cache").Text, "every word must match")
}

func TestTaskCommand_SearchStopsAtThePageCap(t *testing.T) {
	b, host, _ := newActionBot(t)
	for i := 0; i < taskSearchPageSize*taskSearchMaxPages+20; i++ {
		host.addTask(pluginsdk.Task{ID: fmt.Sprintf("bulk-%d", i), Title: "Bulk", UpdatedAt: fmt.Sprintf("2026-10-25T%05d", i)})
	}
	found, err := b.findTasks(context.Background(), "bulk")
	require.NoError(t, err)
	require.Len(t, found, taskSearchPageSize*taskSearchMaxPages)
	require.Equal(t, taskSearchMaxPages, host.data().taskListCalls)
}

func TestMuteAndUnmuteCommands(t *testing.T) {
	b, _, _ := newActionBot(t)
	ctx := context.Background()

	require.Equal(t, "No task is muted.", b.unmuteCommand(ctx, "").Text)
	require.Contains(t, b.muteCommand(ctx, "login").Text, "Several tasks match")
	require.Equal(t, "Muted Fix login. No alerts for it until you send /unmute.", b.muteCommand(ctx, "fix").Text)
	require.Equal(t, "Fix login is already muted.", b.muteCommand(ctx, "fix login").Text)
	require.Equal(t, muteUsage, b.muteCommand(ctx, "").Text)
	b.muteCommand(ctx, "cache")

	list := b.unmuteCommand(ctx, "").Text
	require.Contains(t, list, "- Fix login")
	require.Contains(t, list, "- Add login cache")
	require.Equal(t, "Unmuted Fix login.", b.unmuteCommand(ctx, "fix").Text)
	require.Equal(t, "No muted task matches that. Send /unmute to list them.", b.unmuteCommand(ctx, "fix").Text)
	require.Equal(t, "Unmuted Add login cache.", b.unmuteCommand(ctx, "all").Text, "one task is named")
	require.Equal(t, "No task is muted.", b.unmuteCommand(ctx, "").Text)

	b.muteCommand(ctx, "fix")
	b.muteCommand(ctx, "cache")
	require.Equal(t, "Unmuted 2 tasks.", b.unmuteCommand(ctx, "ALL").Text)
}

func TestPollOnce_RoutesNewCommands(t *testing.T) {
	b, _, tg := newActionBot(t)
	tg.queueUpdates(privateText(1, 42, "/task fix"), privateText(2, 42, "/mute fix"), privateText(3, 42, "/unmute"))
	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	sent := tg.sentMessages()
	require.Len(t, sent, 3)
	require.Contains(t, sent[0].Msg.Text, "Workflow: Solo Dev")
	require.Contains(t, sent[1].Msg.Text, "Muted Fix login")
	require.Contains(t, sent[2].Msg.Text, "- Fix login")
}

func TestHelpMentionsTheNewCommands(t *testing.T) {
	for _, c := range []string{"/task", "/mute", "/unmute", "Mute and Done"} {
		require.Contains(t, helpText, c)
	}
}
