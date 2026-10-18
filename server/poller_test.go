package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func privateText(updateID, userID int64, text string) tgUpdate {
	return tgUpdate{UpdateID: updateID, Message: &tgMessage{
		MessageID: updateID * 10,
		From:      &tgUser{ID: userID},
		Chat:      tgChat{ID: userID, Type: "private"},
		Text:      text,
	}}
}

func newTestBot(t *testing.T, host *fakeHost, tg *fakeTelegram) *bot {
	t.Helper()
	cfg := config{BotToken: testToken, AllowedUserIDs: []int64{42}, KandevURL: testBase, AlertInteractions: true, AlertCompleted: true, AlertFailed: true}
	return newBot(host, tg, cfg, newFakeClock(), "kandev_bot")
}

func TestPollOnce_PersistsOffsetBeforeHandling(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost()
	tg := newFakeTelegram()
	b := newTestBot(t, host, tg)

	var offsetDuringHandling int64
	tg.onSend = func() {
		offsetDuringHandling, _ = b.store.offset(ctx)
	}
	tg.queueUpdates(privateText(100, 42, "/start"))

	next, err := b.pollOnce(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, int64(101), next)
	require.Equal(t, int64(101), offsetDuringHandling, "offset is stored before the update is handled")
	stored, err := b.store.offset(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(101), stored)
	require.Len(t, tg.sentMessages(), 1)
}

func TestPollOnce_RequestsFromGivenOffset(t *testing.T) {
	tg := newFakeTelegram()
	b := newTestBot(t, newFakeHost(), tg)
	tg.queueUpdates()
	_, err := b.pollOnce(context.Background(), 555)
	require.NoError(t, err)
	require.Equal(t, []int64{555}, tg.offsets())
}

func TestPollOnce_DropsNonAllowlistedSendersSilently(t *testing.T) {
	tg := newFakeTelegram()
	b := newTestBot(t, newFakeHost(), tg)
	tg.queueUpdates(privateText(1, 999, "/status"), privateText(2, 999, "/new hack"))

	next, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), next)
	require.Empty(t, tg.sentMessages())
}

func TestPollOnce_IgnoresGroupChatsAndNonMessages(t *testing.T) {
	tg := newFakeTelegram()
	b := newTestBot(t, newFakeHost(), tg)
	group := privateText(1, 42, "/status")
	group.Message.Chat = tgChat{ID: -100, Type: "group"}
	noFrom := privateText(2, 42, "/status")
	noFrom.Message.From = nil
	tg.queueUpdates(group, noFrom, tgUpdate{UpdateID: 3})

	next, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, int64(4), next)
	require.Empty(t, tg.sentMessages())
}

func TestPollOnce_StartAndHelpReplyWithHelp(t *testing.T) {
	tg := newFakeTelegram()
	b := newTestBot(t, newFakeHost(), tg)
	tg.queueUpdates(privateText(1, 42, "/start"), privateText(2, 42, "/help"), privateText(3, 42, "/unknown"), privateText(4, 42, "just text"))

	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	sent := tg.sentMessages()
	require.Len(t, sent, 4)
	for _, m := range sent {
		require.Equal(t, int64(42), m.ChatID)
		require.Contains(t, m.Msg.Text, "/new")
		require.Contains(t, m.Msg.Text, "/status")
		require.NotContains(t, m.Msg.Text, "—")
	}
	require.Equal(t, int64(10), sent[0].ReplyTo)
}

func TestPollOnce_IgnoresCommandsForOtherBots(t *testing.T) {
	tg := newFakeTelegram()
	b := newTestBot(t, newFakeHost(), tg)
	tg.queueUpdates(privateText(1, 42, "/start@other_bot"))
	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	require.Empty(t, tg.sentMessages())
}

func TestPollOnce_SendFailureDoesNotStopTheBatch(t *testing.T) {
	tg := newFakeTelegram()
	tg.sendErr = errors.New("telegram sendMessage: 403 Forbidden: bot was blocked by the user")
	b := newTestBot(t, newFakeHost(), tg)
	tg.queueUpdates(privateText(1, 42, "/start"), privateText(2, 42, "/help"))
	next, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), next)
}

func TestPollLoop_ResumesFromStoredOffsetAfterRestart(t *testing.T) {
	host := newFakeHost()
	require.NoError(t, stateStore{host: host}.setOffset(context.Background(), 77))
	tg := newFakeTelegram()
	b := newTestBot(t, host, tg)

	ctx, cancel := context.WithCancel(context.Background())
	tg.onDrained = cancel
	tg.queueUpdates(privateText(77, 42, "/start"))
	b.pollLoop(ctx)

	offsets := tg.offsets()
	require.Equal(t, int64(77), offsets[0])
	require.Equal(t, int64(78), offsets[len(offsets)-1])
}

func TestPollLoop_BacksOffOnConflictAndKeepsRunning(t *testing.T) {
	tg := newFakeTelegram()
	conflict := &telegramAPIError{Method: "getUpdates", Code: 409, Description: "Conflict"}
	tg.queueError(conflict)
	tg.queueError(conflict)
	tg.queueError(conflict)
	tg.queueUpdates(privateText(1, 42, "/start"))
	host := newFakeHost()
	b := newTestBot(t, host, tg)
	clk := b.clock.(*fakeClock)

	ctx, cancel := context.WithCancel(context.Background())
	tg.onDrained = cancel
	b.pollLoop(ctx)

	require.Equal(t, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}, clk.sleeps())
	require.Len(t, tg.sentMessages(), 1, "the loop recovers after conflicts")
	status, err := b.store.botStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.Connected)
	require.Empty(t, status.LastError)
	require.Equal(t, "2026-10-18T10:00:35Z", status.LastUpdateAt)
}

func TestPollLoop_RecordsErrorInStatusWithoutToken(t *testing.T) {
	tg := newFakeTelegram()
	tg.queueError(&telegramAPIError{Method: "getUpdates", Code: 409, Description: "Conflict: terminated by other getUpdates request"})
	host := newFakeHost()
	b := newTestBot(t, host, tg)
	clk := b.clock.(*fakeClock)
	ctx, cancel := context.WithCancel(context.Background())
	clk.onSleep = cancel
	b.pollLoop(ctx)

	status, err := b.store.botStatus(context.Background())
	require.NoError(t, err)
	require.Contains(t, status.LastError, "Conflict")
	require.NotContains(t, status.LastError, testToken)
}

func TestBackoff(t *testing.T) {
	var b backoff
	var got []time.Duration
	for i := 0; i < 6; i++ {
		got = append(got, b.next())
	}
	require.Equal(t, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second}, got)
	b.reset()
	require.Equal(t, 5*time.Second, b.next())
}

func TestPollOnce_OffsetWriteFailureStopsBeforeHandling(t *testing.T) {
	host := newFakeHost()
	host.setStateErr = map[string]error{stateKeyOffset: errors.New("state unavailable")}
	tg := newFakeTelegram()
	b := newTestBot(t, host, tg)
	tg.queueUpdates(privateText(5, 42, "/start"), privateText(6, 42, "/start"))

	next, err := b.pollOnce(context.Background(), 5)
	require.Error(t, err)
	require.Equal(t, int64(5), next, "the batch is fetched again from the old offset")
	require.Empty(t, tg.sentMessages(), "an update whose offset was not stored is not handled")
}

func TestPollLoop_UnauthorizedClearsConnected(t *testing.T) {
	tg := newFakeTelegram()
	tg.queueError(&telegramAPIError{Method: "getUpdates", Code: 401, Description: "Unauthorized"})
	host := newFakeHost()
	b := newTestBot(t, host, tg)
	clk := b.clock.(*fakeClock)
	ctx, cancel := context.WithCancel(context.Background())
	clk.onSleep = cancel
	b.pollLoop(ctx)

	status, err := b.store.botStatus(context.Background())
	require.NoError(t, err)
	require.False(t, status.Connected)
	require.Contains(t, status.LastError, "401")
}
