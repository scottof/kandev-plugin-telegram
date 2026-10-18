package main

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// Bot messages are plain text (no parse_mode), so titles never need escaping.
const (
	maxMessageRunes    = 4096 // Telegram's sendMessage text limit
	maxButtons         = 10
	maxButtonTextRunes = 40
	maxTitleRunes      = 120
	maxDetailRunes     = 300
	maxListedAlerts    = 10
)

type alertKind string

const (
	alertPermission    alertKind = "permission"
	alertClarification alertKind = "clarification"
	alertCompleted     alertKind = "completed"
	alertFailed        alertKind = "failed"
	alertReview        alertKind = "review"
)

var alertLabels = map[alertKind]string{
	alertPermission:    "Needs permission",
	alertClarification: "Has a question",
	alertCompleted:     "Finished",
	alertFailed:        "Failed",
	alertReview:        "Waiting for review",
}

// alert is one thing worth telling the allowed users about.
type alert struct {
	Kind      alertKind
	TaskID    string
	TaskTitle string
	Detail    string
}

// linkButton is an inline keyboard URL button.
type linkButton struct {
	Text string
	URL  string
}

// outMessage is a bot message before it is addressed to a chat.
type outMessage struct {
	Text    string
	Buttons []linkButton
}

// interactionAlert describes a pending permission or clarification: the
// permission title and action type, or the first line of the first question.
func interactionAlert(in pluginsdk.Interaction, taskTitle string) alert {
	a := alert{Kind: alertPermission, TaskID: in.TaskID, TaskTitle: taskTitle}
	if in.Kind == pluginsdk.InteractionKindClarification {
		a.Kind = alertClarification
		if len(in.Questions) > 0 {
			a.Detail = firstLine(in.Questions[0].Prompt)
			if a.Detail == "" {
				a.Detail = firstLine(in.Questions[0].Title)
			}
		}
		if a.Detail == "" {
			a.Detail = firstLine(in.Title)
		}
		return a
	}
	a.Detail = firstLine(in.Title)
	if in.ActionType != "" {
		if a.Detail == "" {
			a.Detail = in.ActionType
		} else {
			a.Detail = fmt.Sprintf("%s (%s)", a.Detail, in.ActionType)
		}
	}
	return a
}

// formatAlerts renders one alert in full, or a burst as a single list so a
// busy moment never floods the chat or hits the rate limit.
func formatAlerts(alerts []alert, base string) outMessage {
	if len(alerts) == 1 {
		a := alerts[0]
		text := alertLabels[a.Kind] + ": " + truncateRunes(a.TaskTitle, maxTitleRunes)
		if a.Detail != "" {
			text += "\n" + truncateRunes(a.Detail, maxDetailRunes)
		}
		return outMessage{
			Text:    capMessage(text),
			Buttons: []linkButton{{Text: "Open task", URL: taskURL(base, a.TaskID)}},
		}
	}

	lines := []string{fmt.Sprintf("%d updates from kandev:", len(alerts))}
	for i, a := range alerts {
		if i == maxListedAlerts {
			lines = append(lines, fmt.Sprintf("and %d more", len(alerts)-maxListedAlerts))
			break
		}
		line := "- " + alertLabels[a.Kind] + ": " + truncateRunes(a.TaskTitle, maxTitleRunes)
		if a.Detail != "" {
			line += ". " + truncateRunes(a.Detail, maxDetailRunes)
		}
		lines = append(lines, line)
	}
	return outMessage{Text: capMessage(strings.Join(lines, "\n")), Buttons: taskButtons(alerts, base)}
}

// taskButtons builds one button per distinct task, in order, up to maxButtons.
func taskButtons(alerts []alert, base string) []linkButton {
	var buttons []linkButton
	seen := map[string]bool{}
	for _, a := range alerts {
		if len(buttons) == maxButtons {
			break
		}
		if seen[a.TaskID] {
			continue
		}
		seen[a.TaskID] = true
		buttons = append(buttons, linkButton{Text: truncateRunes(a.TaskTitle, maxButtonTextRunes), URL: taskURL(base, a.TaskID)})
	}
	return buttons
}

func taskURL(base, taskID string) string {
	return strings.TrimRight(base, "/") + "/t/" + url.PathEscape(taskID)
}

func inboxURL(base string) string {
	return strings.TrimRight(base, "/") + "/needs-you-inbox"
}

// truncateRunes shortens s to at most max runes, ending with an ellipsis.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

func capMessage(text string) string {
	return truncateRunes(text, maxMessageRunes)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
