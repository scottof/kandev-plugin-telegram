package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	maxStatusLines  = 10
	statusTaskLimit = 50
)

// statusEntry is one /status line; At orders the list newest first.
type statusEntry struct {
	alert
	At string
}

// statusMessage lists running tasks and pending interactions. A source that
// fails is reported in the message instead of failing the whole reply.
func (b *bot) statusMessage(ctx context.Context) outMessage {
	var entries []statusEntry
	var notes []string
	waiting, running := 0, 0

	tasks, _, err := b.host.Tasks().List(ctx, pluginsdk.TaskFilter{States: []string{"IN_PROGRESS"}}, pluginsdk.Page{Limit: statusTaskLimit})
	if err != nil {
		notes = append(notes, "Could not read running tasks.")
	}
	for _, t := range tasks {
		title := t.Title
		if title == "" {
			title = untitledTask
		}
		entries = append(entries, statusEntry{alert: alert{Kind: alertRunning, TaskID: t.ID, TaskTitle: title}, At: t.UpdatedAt})
		running++
	}

	pending, err := b.pendingInteractions(ctx)
	if err != nil {
		notes = append(notes, "Could not read agents waiting on you.")
	}
	titles := map[string]string{}
	for _, in := range pending {
		entries = append(entries, statusEntry{alert: interactionAlert(in, b.taskTitle(ctx, in.TaskID, titles)), At: in.CreatedAt})
		waiting++
	}

	return formatStatus(entries, waiting, running, b.cfg.KandevURL, notes...)
}

func (b *bot) pendingInteractions(ctx context.Context) ([]pluginsdk.Interaction, error) {
	accessor, ok := pluginsdk.Interactions(b.host)
	if !ok {
		return nil, fmt.Errorf("host has no interactions API")
	}
	return listAllPending(ctx, accessor)
}

func formatStatus(entries []statusEntry, waiting, running int, base string, notes ...string) outMessage {
	inbox := linkButton{Text: "Open needs-you inbox", URL: inboxURL(base)}
	if len(entries) == 0 {
		text := "Nothing is running and no agent is waiting on you."
		if len(notes) > 0 {
			text = strings.Join(notes, "\n")
		}
		return outMessage{Text: text, Buttons: []linkButton{inbox}}
	}

	sorted := append([]statusEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return parseTime(sorted[i].At).After(parseTime(sorted[j].At)) })

	lines := []string{fmt.Sprintf("Waiting on you: %d. Running: %d.", waiting, running)}
	alerts := make([]alert, 0, len(sorted))
	for i, e := range sorted {
		if i == maxStatusLines {
			lines = append(lines, fmt.Sprintf("and %d more", len(sorted)-maxStatusLines))
			break
		}
		line := "- " + alertLabels[e.Kind] + ": " + truncateRunes(e.TaskTitle, maxTitleRunes)
		if e.Detail != "" {
			line += ". " + truncateRunes(e.Detail, maxDetailRunes)
		}
		lines = append(lines, line)
		alerts = append(alerts, e.alert)
	}
	lines = append(lines, notes...)
	lines = append(lines, "Full list: "+inboxURL(base))

	buttons := taskButtons(alerts, base)
	if len(buttons) > maxButtons-1 {
		buttons = buttons[:maxButtons-1]
	}
	return outMessage{Text: capMessage(strings.Join(lines, "\n")), Buttons: append(buttons, inbox)}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
