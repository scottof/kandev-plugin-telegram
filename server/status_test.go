package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestFormatStatus_Empty(t *testing.T) {
	msg := formatStatus(nil, 0, 0, testBase)
	require.Equal(t, "Nothing is running and no agent is waiting on you.", msg.Text)
	require.Equal(t, []linkButton{{Text: "Open needs-you inbox", URL: testBase + "/needs-you-inbox"}}, msg.Buttons)
}

func TestFormatStatus_SortsNewestFirstAndLinks(t *testing.T) {
	entries := []statusEntry{
		{alert: alert{Kind: alertRunning, TaskID: "task-2", TaskTitle: "Add cache"}, At: "2026-10-18T09:00:00Z"},
		{alert: alert{Kind: alertPermission, TaskID: "task-1", TaskTitle: "Fix login", Detail: "Run npm test (execute)"}, At: "2026-10-18T09:30:00Z"},
	}
	msg := formatStatus(entries, 1, 1, testBase)
	require.Equal(t, strings.Join([]string{
		"Waiting on you: 1. Running: 1.",
		"- Needs permission: Fix login. Run npm test (execute)",
		"- Running: Add cache",
		"Full list: " + testBase + "/needs-you-inbox",
	}, "\n"), msg.Text)
	require.Equal(t, []linkButton{
		{Text: "Fix login", URL: testBase + "/t/task-1"},
		{Text: "Add cache", URL: testBase + "/t/task-2"},
		{Text: "Open needs-you inbox", URL: testBase + "/needs-you-inbox"},
	}, msg.Buttons)
}

func TestFormatStatus_CapsAtTenLines(t *testing.T) {
	var entries []statusEntry
	for i := 0; i < 14; i++ {
		entries = append(entries, statusEntry{
			alert: alert{Kind: alertRunning, TaskID: fmt.Sprintf("task-%02d", i), TaskTitle: fmt.Sprintf("Task %02d", i)},
			At:    fmt.Sprintf("2026-10-18T09:%02d:00Z", i),
		})
	}
	msg := formatStatus(entries, 0, 14, testBase)
	lines := strings.Split(msg.Text, "\n")
	require.Equal(t, "- Running: Task 13", lines[1], "newest first")
	require.Equal(t, "and 4 more", lines[len(lines)-2])
	require.Len(t, lines, 1+maxStatusLines+2)
	require.Len(t, msg.Buttons, maxButtons)
	require.Equal(t, "Open needs-you inbox", msg.Buttons[maxButtons-1].Text)
	require.NotContains(t, msg.Text, "—")
}

func TestStatusMessage_CombinesRunningTasksAndPendingInteractions(t *testing.T) {
	b, host, _ := newAlertBot(t)
	host.addTask(pluginsdk.Task{ID: "task-3", Title: "Write docs", State: "IN_PROGRESS", UpdatedAt: "2026-10-18T08:00:00Z"})
	host.addTask(pluginsdk.Task{ID: "task-4", Title: "Old", State: "COMPLETED", UpdatedAt: "2026-10-18T09:59:00Z"})
	p := permission("i1", "task-1", "Run npm test")
	p.CreatedAt = "2026-10-18T09:00:00Z"
	host.setPending(p)

	msg := b.statusMessage(context.Background())
	require.Equal(t, strings.Join([]string{
		"Waiting on you: 1. Running: 1.",
		"- Needs permission: Fix login. Run npm test (execute)",
		"- Running: Write docs",
		"Full list: " + testBase + "/needs-you-inbox",
	}, "\n"), msg.Text)
	require.Equal(t, []string{"IN_PROGRESS"}, host.data().lastTaskFilter.States)
}

func TestStatusMessage_ReportsPartialFailure(t *testing.T) {
	b, host, _ := newAlertBot(t)
	host.addTask(pluginsdk.Task{ID: "task-3", Title: "Write docs", State: "IN_PROGRESS"})
	host.data().listPendingErr = errors.New("unavailable")

	msg := b.statusMessage(context.Background())
	require.Contains(t, msg.Text, "- Running: Write docs")
	require.Contains(t, msg.Text, "Could not read agents waiting on you.")
}

func TestPollOnce_StatusCommandReplies(t *testing.T) {
	host := newFakeHost()
	host.addTask(pluginsdk.Task{ID: "task-3", Title: "Write docs", State: "IN_PROGRESS"})
	tg := newFakeTelegram()
	b := newTestBot(t, host, tg)
	tg.queueUpdates(privateText(1, 42, "/status"))
	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Contains(t, sent[0].Msg.Text, "- Running: Write docs")
	require.Equal(t, int64(10), sent[0].ReplyTo)
}
