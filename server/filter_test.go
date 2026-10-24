package main

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestParseQuietHours(t *testing.T) {
	off, err := parseQuietHours("", "")
	require.NoError(t, err)
	require.Nil(t, off)
	require.False(t, off.active(time.Now()), "a nil window is never active")

	q, err := parseQuietHours(" 22:00 - 07:30 ", "")
	require.NoError(t, err)
	require.Equal(t, 22*60, q.start)
	require.Equal(t, 7*60+30, q.end)
	require.Equal(t, "Europe/Amsterdam", q.loc.String())

	for _, bad := range []string{"22-07", "22:00", "25:00-07:00", "22:00-07:60", "08:00-08:00", "22:0-07:00"} {
		_, err := parseQuietHours(bad, "")
		require.ErrorIs(t, err, errQuietHours, bad)
	}
	_, err = parseQuietHours("22:00-07:00", "Mars/Olympus")
	require.ErrorContains(t, err, "timezone")
}

func TestQuietHoursActive(t *testing.T) {
	night, err := parseQuietHours("22:00-07:00", "Europe/Amsterdam")
	require.NoError(t, err)
	// Amsterdam is UTC+2 in October before the switch.
	at := func(hhmm string) time.Time {
		tm, err := time.Parse("2006-01-02 15:04", "2026-10-24 "+hhmm)
		require.NoError(t, err)
		return tm
	}
	require.True(t, night.active(at("20:00")), "22:00 Amsterdam")
	require.True(t, night.active(at("04:59")), "06:59 Amsterdam")
	require.False(t, night.active(at("05:00")), "07:00 Amsterdam ends it")
	require.False(t, night.active(at("19:59")))

	lunch, err := parseQuietHours("12:00-13:00", "UTC")
	require.NoError(t, err)
	require.True(t, lunch.active(at("12:30")))
	require.False(t, lunch.active(at("13:00")))
}

func TestLoadConfig_QuietHoursAndWorkflowFilter(t *testing.T) {
	with := func(extra map[string]any) map[string]any {
		raw := validConfig()
		for k, v := range extra {
			raw[k] = v
		}
		return raw
	}
	cfg, err := loadConfig(with(map[string]any{"quiet_hours": "22:00-07:00", "alert_workflows": " Solo Dev, ,shape up "}))
	require.NoError(t, err)
	require.NotNil(t, cfg.QuietHours)
	require.Equal(t, []string{"solo dev", "shape up"}, cfg.AlertWorkflows)

	_, err = loadConfig(with(map[string]any{"quiet_hours": "late"}))
	require.ErrorIs(t, err, errQuietHours)

	cfg, err = loadConfig(validConfig())
	require.NoError(t, err)
	require.Nil(t, cfg.QuietHours, "quiet hours are off by default")
	require.Empty(t, cfg.AlertWorkflows)
}

func TestFlushAlerts_QuietHoursHoldAlertsUntilTheyEnd(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newActionBot(t)
	q, err := parseQuietHours("09:00-11:00", "UTC")
	require.NoError(t, err)
	b.cfg.QuietHours = q // the fake clock starts at 10:00 UTC
	host.setPending(permission("i1", "task-1", "Run npm test"))
	_, _, err = b.taskAlertFromEvent(ctx, stateChanged("task-2", "IN_PROGRESS", "COMPLETED", "t1"))
	require.NoError(t, err)

	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages(), "nothing goes out during quiet hours")
	queue, err := b.store.taskAlertQueue(ctx)
	require.NoError(t, err)
	require.Len(t, queue, 1, "task alerts stay queued")

	b.clock.(*fakeClock).advance(time.Hour)
	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Contains(t, sent[0].Msg.Text, "2 updates from kandev")
}

func TestFlushAlerts_MutedTaskIsSkippedAndNotReplayed(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newActionBot(t)
	_, err := b.mute(ctx, "task-1")
	require.NoError(t, err)
	host.setPending(permission("i1", "task-1", "Run npm test"))
	_, _, err = b.taskAlertFromEvent(ctx, stateChanged("task-1", "IN_PROGRESS", "COMPLETED", "t1"))
	require.NoError(t, err)

	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages())
	queue, err := b.store.taskAlertQueue(ctx)
	require.NoError(t, err)
	require.Empty(t, queue, "a muted task's alert is settled, not kept")

	require.NoError(t, b.unmute(ctx, []string{"task-1"}))
	b.flushAlerts(ctx)
	require.Empty(t, tg.sentMessages(), "unmuting does not replay the prompt that was pending while muted")
}

func TestFlushAlerts_WorkflowFilter(t *testing.T) {
	ctx := context.Background()
	b, host, tg := newActionBot(t)
	b.cfg.AlertWorkflows = []string{"solo dev"}
	// task-1 is in Solo Dev, task-2 in Development.
	host.setPending(permission("i1", "task-1", "Run npm test"), permission("i2", "task-2", "Push"))
	payload := stateChanged("task-2", "IN_PROGRESS", "COMPLETED", "t1")
	payload["workflow_id"] = "wf-2"
	_, _, err := b.taskAlertFromEvent(ctx, payload)
	require.NoError(t, err)

	b.flushAlerts(ctx)
	sent := tg.sentMessages()
	require.Len(t, sent, 1)
	require.Equal(t, "Needs permission: Fix login\nRun npm test (execute)", sent[0].Msg.Text)
}

func TestAlertFilter_UnknownWorkflowIsNotDropped(t *testing.T) {
	ctx := context.Background()
	b, host, _ := newActionBot(t)
	b.cfg.AlertWorkflows = []string{"solo dev"}
	host.addTask(pluginsdk.Task{ID: "task-x", Title: "Orphan", WorkflowID: "wf-gone"})
	f := b.newAlertFilter(ctx)
	require.False(t, f.dropped(ctx, "task-x", ""))
	require.False(t, f.dropped(ctx, "task-missing", ""), "a task that cannot be read is not dropped")
	require.True(t, f.dropped(ctx, "task-2", ""))
}

func TestStatus_NotesMutedTasksAndQuietHours(t *testing.T) {
	ctx := context.Background()
	b, _, _ := newActionBot(t)
	_, err := b.mute(ctx, "task-1")
	require.NoError(t, err)
	q, err := parseQuietHours("09:00-11:30", "UTC")
	require.NoError(t, err)
	b.cfg.QuietHours = q

	text := b.statusMessage(ctx).Text
	require.Contains(t, text, "1 task is muted. /unmute lists them.")
	require.Contains(t, text, "Quiet hours until 11:30: alerts are held until then.")
}

func TestSetHost_RegistersTheCommandMenu(t *testing.T) {
	host := newFakeHost()
	host.config = validConfig()
	tg := newFakeTelegram()
	ctx, cancel := context.WithCancel(context.Background())
	tg.onDrained = cancel
	p, _ := newTestPlugin(ctx, tg)

	p.SetHost(host)
	p.wait(2 * time.Second)

	tg.mu.Lock()
	defer tg.mu.Unlock()
	require.Equal(t, botCommands, tg.commands)
}
