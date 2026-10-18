package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

const testBase = "https://kandev.example"

func TestTruncateRunes(t *testing.T) {
	require.Equal(t, "short", truncateRunes("short", 10))
	require.Equal(t, "abcd…", truncateRunes("abcdefgh", 5))
	// Multi-byte runes are never split.
	got := truncateRunes("🚀🚀🚀🚀🚀🚀", 4)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, "🚀🚀🚀…", got)
	got = truncateRunes("日本語のタスク名です", 5)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, 5, utf8.RuneCountInString(got))
}

func TestTaskURL(t *testing.T) {
	require.Equal(t, "https://kandev.example/t/task-1", taskURL("https://kandev.example", "task-1"))
	require.Equal(t, "https://kandev.example/t/task-1", taskURL("https://kandev.example/", "task-1"))
	require.Equal(t, "https://kandev.example/needs-you-inbox", inboxURL("https://kandev.example/"))
}

func TestInteractionAlert_Permission(t *testing.T) {
	a := interactionAlert(pluginsdk.Interaction{
		ID: "i1", Kind: pluginsdk.InteractionKindPermission, TaskID: "task-1",
		Title: "Run npm test", ActionType: "execute",
	}, "Fix login")
	require.Equal(t, alert{Kind: alertPermission, TaskID: "task-1", TaskTitle: "Fix login", Detail: "Run npm test (execute)"}, a)
}

func TestInteractionAlert_ClarificationUsesFirstLineOfFirstQuestion(t *testing.T) {
	a := interactionAlert(pluginsdk.Interaction{
		ID: "i2", Kind: pluginsdk.InteractionKindClarification, TaskID: "task-2",
		Questions: []pluginsdk.InteractionQuestion{{Prompt: "\nWhich database?\nWe have two options."}},
	}, "Add cache")
	require.Equal(t, alertClarification, a.Kind)
	require.Equal(t, "Which database?", a.Detail)

	a = interactionAlert(pluginsdk.Interaction{
		Kind: pluginsdk.InteractionKindClarification, Title: "Fallback title",
	}, "Add cache")
	require.Equal(t, "Fallback title", a.Detail)
}

func TestFormatAlerts_Single(t *testing.T) {
	msg := formatAlerts([]alert{{Kind: alertPermission, TaskID: "task-1", TaskTitle: "Fix login", Detail: "Run npm test (execute)"}}, testBase)
	require.Equal(t, "Needs permission: Fix login\nRun npm test (execute)", msg.Text)
	require.Equal(t, []linkButton{{Text: "Open task", URL: testBase + "/t/task-1"}}, msg.Buttons)
}

func TestFormatAlerts_SingleWithoutDetail(t *testing.T) {
	msg := formatAlerts([]alert{{Kind: alertCompleted, TaskID: "task-1", TaskTitle: "Fix login"}}, testBase)
	require.Equal(t, "Finished: Fix login", msg.Text)
}

func TestFormatAlerts_BatchesManyAlertsIntoOneMessage(t *testing.T) {
	var alerts []alert
	for i := 0; i < 13; i++ {
		alerts = append(alerts, alert{Kind: alertFailed, TaskID: fmt.Sprintf("task-%d", i), TaskTitle: fmt.Sprintf("Task %d", i)})
	}
	msg := formatAlerts(alerts, testBase)
	lines := strings.Split(msg.Text, "\n")
	require.Equal(t, "13 updates from kandev:", lines[0])
	require.Equal(t, "- Failed: Task 0", lines[1])
	require.Equal(t, "and 3 more", lines[len(lines)-1])
	require.Len(t, msg.Buttons, maxButtons)
	require.Equal(t, "Task 0", msg.Buttons[0].Text)
	require.Equal(t, testBase+"/t/task-0", msg.Buttons[0].URL)
}

func TestFormatAlerts_OneButtonPerTask(t *testing.T) {
	msg := formatAlerts([]alert{
		{Kind: alertPermission, TaskID: "task-1", TaskTitle: "A", Detail: "x"},
		{Kind: alertClarification, TaskID: "task-1", TaskTitle: "A", Detail: "y"},
	}, testBase)
	require.Len(t, msg.Buttons, 1)
}

func TestFormatAlerts_TruncatesLongFieldsAndCapsMessage(t *testing.T) {
	long := strings.Repeat("x", 10000)
	msg := formatAlerts([]alert{{Kind: alertPermission, TaskID: "t", TaskTitle: long, Detail: long}}, testBase)
	require.LessOrEqual(t, utf8.RuneCountInString(msg.Text), maxMessageRunes)
	require.LessOrEqual(t, utf8.RuneCountInString(msg.Buttons[0].Text), maxButtonTextRunes)
	require.Less(t, utf8.RuneCountInString(msg.Text), 1000)
}

func TestCapMessage(t *testing.T) {
	require.Equal(t, maxMessageRunes, utf8.RuneCountInString(capMessage(strings.Repeat("é", 5000))))
}

func TestFormatAlerts_NoEmDash(t *testing.T) {
	for _, kind := range []alertKind{alertPermission, alertClarification, alertCompleted, alertFailed, alertReview} {
		single := formatAlerts([]alert{{Kind: kind, TaskID: "t", TaskTitle: "T", Detail: "d"}}, testBase)
		multi := formatAlerts([]alert{{Kind: kind, TaskID: "t", TaskTitle: "T"}, {Kind: kind, TaskID: "u", TaskTitle: "U"}}, testBase)
		require.NotContains(t, single.Text, "—")
		require.NotContains(t, multi.Text, "—")
	}
}
