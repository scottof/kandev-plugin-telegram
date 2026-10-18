package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateStore_OffsetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := stateStore{host: newFakeHost()}

	offset, err := store.offset(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), offset)

	// Telegram update ids exceed float64's exact integer range only past 2^53,
	// far above real values, but they must survive the float64 round trip.
	require.NoError(t, store.setOffset(ctx, 987654321012))
	offset, err = store.offset(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(987654321012), offset)
}

func TestStateStore_SentInteractionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := stateStore{host: newFakeHost()}

	sent, err := store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Empty(t, sent)

	want := sentSet{42: {"b": true, "a": true}, 987654321: {"a": true}}
	require.NoError(t, store.setSentInteractions(ctx, want))
	sent, err = store.sentInteractions(ctx)
	require.NoError(t, err)
	require.Equal(t, want, sent)
}

func TestStateStore_TaskAlertQueueKeepsSettledUsers(t *testing.T) {
	ctx := context.Background()
	store := stateStore{host: newFakeHost()}
	want := []queuedTaskAlert{{
		alert:      alert{Kind: alertCompleted, TaskID: "t1", TaskTitle: "Fix login"},
		Marker:     "COMPLETED@1",
		SettledFor: []int64{42, 987654321},
	}}
	require.NoError(t, store.setTaskAlertQueue(ctx, want))
	queue, err := store.taskAlertQueue(ctx)
	require.NoError(t, err)
	require.Equal(t, want, queue)
}

func TestStateStore_TaskAlertLogRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := stateStore{host: newFakeHost()}

	log, err := store.taskAlerts(ctx)
	require.NoError(t, err)
	require.False(t, log.seen("t1", "COMPLETED@1"))

	log = log.add("t1", "COMPLETED@1")
	require.NoError(t, store.setTaskAlerts(ctx, log))
	log, err = store.taskAlerts(ctx)
	require.NoError(t, err)
	require.True(t, log.seen("t1", "COMPLETED@1"))
	require.False(t, log.seen("t1", "REVIEW@2"))
}

func TestTaskAlertLog_ReplacesEntryForSameTask(t *testing.T) {
	var log taskAlertLog
	log = log.add("t1", "REVIEW@1")
	log = log.add("t1", "COMPLETED@2")
	require.Len(t, log, 1)
	require.True(t, log.seen("t1", "COMPLETED@2"))
	require.False(t, log.seen("t1", "REVIEW@1"))
}

func TestTaskAlertLog_CapsOldestEntries(t *testing.T) {
	var log taskAlertLog
	for i := 0; i < taskAlertLogCap+5; i++ {
		log = log.add(fmt.Sprintf("t%d", i), "COMPLETED@1")
	}
	require.Len(t, log, taskAlertLogCap)
	require.False(t, log.seen("t0", "COMPLETED@1"))
	require.True(t, log.seen(fmt.Sprintf("t%d", taskAlertLogCap+4), "COMPLETED@1"))
}

func TestStateStore_BotStatusRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := stateStore{host: newFakeHost()}

	status, err := store.botStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, botStatus{}, status)

	want := botStatus{Connected: true, Username: "kandev_bot", LastUpdateAt: "2026-10-18T10:00:00Z", LastError: "boom"}
	require.NoError(t, store.setBotStatus(ctx, want))
	status, err = store.botStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, want, status)
}
