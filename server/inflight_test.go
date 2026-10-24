package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInflight_ClearedAfterEachCommand(t *testing.T) {
	b, host, tg := newTriageBot(t)
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"Write docs"}`
	tg.queueUpdates(privateText(1, 42, "/status"), privateText(2, 42, "/new write the docs"))

	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	_, ok, err := b.inflight(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
}

func TestInflight_RecordsTheCommandAndCreatedTask(t *testing.T) {
	b, host, tg := newTriageBot(t)
	d := host.data()
	d.agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"Write docs"}`
	var during inflightCommand
	var recorded bool
	tg.onSend = func() { during, recorded, _ = b.inflight(context.Background()) }
	tg.queueUpdates(privateText(1, 42, "/new write the docs"))

	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	require.True(t, recorded, "the record exists while the reply is sent")
	require.Equal(t, inflightCommand{ChatID: 42, MessageID: 10, Command: "new", TaskID: "task-new"}, during)
}

func TestReportInterrupted_NewBeforeTheTask(t *testing.T) {
	b, _, tg := newTriageBot(t)
	ctx := context.Background()
	b.beginCommand(ctx, &tgMessage{MessageID: 10, Chat: tgChat{ID: 42}}, "new")

	b.reportInterrupted(ctx)

	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Equal(t, int64(42), sent[0].ChatID)
	require.Equal(t, int64(10), sent[0].ReplyTo)
	require.Equal(t, "A plugin restart interrupted your /new before a task was created. Send it again.", sent[0].Msg.Text)
	_, ok, err := b.inflight(ctx)
	require.NoError(t, err)
	require.False(t, ok, "the notice is sent once")

	b.reportInterrupted(ctx)
	require.Len(t, tg.sentMessages(), 1)
}

func TestReportInterrupted_NewAfterTheTask(t *testing.T) {
	b, _, tg := newTriageBot(t)
	ctx := context.Background()
	b.beginCommand(ctx, &tgMessage{MessageID: 10, Chat: tgChat{ID: 42}}, "new")
	b.markInflightTask(ctx, "task-1")

	b.reportInterrupted(ctx)

	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Contains(t, sent[0].Msg.Text, "after the task was created, so do not send it again")
	require.Equal(t, []linkButton{{Text: "Open task", URL: testBase + "/t/task-1"}}, sent[0].Msg.Buttons)
}

func TestReportInterrupted_Status(t *testing.T) {
	b, _, tg := newTriageBot(t)
	ctx := context.Background()
	b.beginCommand(ctx, &tgMessage{MessageID: 30, Chat: tgChat{ID: 42}}, "status")
	b.reportInterrupted(ctx)
	require.Equal(t, "A plugin restart interrupted your /status. Send it again.", tg.sentMessages()[0].Msg.Text)
}

func TestSetHost_ReportsACommandCutShortByARestart(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	require.NoError(t, host.SetState(context.Background(), stateScope, "", stateKeyInflight,
		map[string]any{"chat_id": int64(42), "message_id": int64(10), "command": "new", "task_id": ""}))
	tg := newFakeTelegram()
	ctx, cancel := context.WithCancel(context.Background())
	tg.onDrained = cancel
	p, _ := newTestPlugin(ctx, tg)

	p.SetHost(host)
	p.wait(2 * time.Second)

	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Equal(t, "A plugin restart interrupted your /new before a task was created. Send it again.", sent[0].Msg.Text)
	require.Equal(t, int64(10), sent[0].ReplyTo)
}

func TestReportInterrupted_NothingInFlight(t *testing.T) {
	b, _, tg := newTriageBot(t)
	b.reportInterrupted(context.Background())
	require.Empty(t, tg.sentMessages())
}

func TestMarkInflightTask_WithoutARecordIsANoOp(t *testing.T) {
	b, _, _ := newTriageBot(t)
	b.markInflightTask(context.Background(), "task-1")
	_, ok, err := b.inflight(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
}
