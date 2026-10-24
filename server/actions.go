package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	// Callback data is "<action>:<task id>"; Telegram allows 64 bytes and a
	// task id is a 36-byte UUID.
	actionMute = "m"
	actionDone = "d"

	stateKeyMuted = "muted_tasks"
	// mutedCap bounds the muted task list kept in Host state.
	mutedCap = 200

	taskSearchPageSize = 100
	taskSearchMaxPages = 5
	maxListedTasks     = 10
)

// taskActions are the action buttons for one task: Mute, and Done unless the
// task already finished.
func taskActions(taskID string, kind alertKind) []linkButton {
	buttons := []linkButton{{Text: "Mute", Data: actionMute + ":" + taskID}}
	if kind != alertCompleted {
		buttons = append(buttons, linkButton{Text: "Done", Data: actionDone + ":" + taskID})
	}
	return buttons
}

// handleCallback runs an action button press from an allowed user and always
// answers it, so the button stops spinning.
func (b *bot) handleCallback(ctx context.Context, q *tgCallbackQuery) {
	if !b.cfg.isAllowed(q.From.ID) {
		return
	}
	notice := b.runAction(ctx, q.Data)
	if err := b.tg.AnswerCallback(ctx, q.ID, notice); err != nil && ctx.Err() == nil {
		log.Printf("telegram: answering a button: %v", err)
	}
}

func (b *bot) runAction(ctx context.Context, data string) string {
	action, taskID, ok := strings.Cut(data, ":")
	if !ok || taskID == "" {
		return "This button no longer works."
	}
	task, err := b.host.Tasks().Get(ctx, taskID)
	if err != nil || task == nil {
		return "That task no longer exists."
	}
	title := truncateRunes(taskLabel(task), maxTitleRunes)
	switch action {
	case actionMute:
		added, err := b.mute(ctx, taskID)
		switch {
		case err != nil:
			return "Could not mute it: " + truncateRunes(err.Error(), maxReasonRunes)
		case !added:
			return title + " is already muted. Send /unmute to undo."
		default:
			return "Muted " + title + ". Send /unmute to undo."
		}
	case actionDone:
		return b.markDone(ctx, task, title)
	default:
		return "This button no longer works."
	}
}

// markDone moves task to its workflow's column for finished work: one named
// like Done, else the last column. The SDK does not say which step completes
// tasks.
func (b *bot) markDone(ctx context.Context, task *pluginsdk.Task, title string) string {
	steps, err := b.host.Workflows().ListSteps(ctx, task.WorkflowID)
	if err != nil || len(steps) == 0 {
		return "Could not read the task's columns."
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	done := steps[len(steps)-1]
	for _, s := range steps {
		if isFinishingColumn(s.Name) {
			done = s
			break
		}
	}
	if task.WorkflowStepID == done.ID {
		return title + " is already in " + done.Name + "."
	}
	if _, err := b.host.Tasks().Move(ctx, pluginsdk.MoveTaskInput{TaskID: task.ID, WorkflowStepID: done.ID}); err != nil {
		return "Could not move it: " + truncateRunes(err.Error(), maxReasonRunes)
	}
	return "Moved " + title + " to " + done.Name + "."
}

func taskLabel(t *pluginsdk.Task) string {
	if t.Title != "" {
		return t.Title
	}
	return untitledTask
}

// mute adds taskID to the muted list; added is false when it was there.
func (b *bot) mute(ctx context.Context, taskID string) (bool, error) {
	b.mutedMu.Lock()
	defer b.mutedMu.Unlock()
	muted, err := b.store.mutedTasks(ctx)
	if err != nil {
		return false, err
	}
	if muted.has(taskID) {
		return false, nil
	}
	muted = append(muted, taskID)
	if len(muted) > mutedCap {
		muted = muted[len(muted)-mutedCap:]
	}
	return true, b.store.setMutedTasks(ctx, muted)
}

func (b *bot) unmute(ctx context.Context, taskIDs []string) error {
	b.mutedMu.Lock()
	defer b.mutedMu.Unlock()
	muted, err := b.store.mutedTasks(ctx)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, id := range taskIDs {
		drop[id] = true
	}
	kept := muted[:0:0]
	for _, id := range muted {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	return b.store.setMutedTasks(ctx, kept)
}

type mutedList []string

func (m mutedList) has(taskID string) bool {
	for _, id := range m {
		if id == taskID {
			return true
		}
	}
	return false
}

func (s stateStore) mutedTasks(ctx context.Context) (mutedList, error) {
	value, err := s.get(ctx, stateKeyMuted)
	if err != nil {
		return nil, err
	}
	var muted mutedList
	items, _ := value["ids"].([]any)
	for _, raw := range items {
		if id, ok := raw.(string); ok && id != "" {
			muted = append(muted, id)
		}
	}
	return muted, nil
}

func (s stateStore) setMutedTasks(ctx context.Context, muted mutedList) error {
	items := make([]any, 0, len(muted))
	for _, id := range muted {
		items = append(items, id)
	}
	return s.set(ctx, stateKeyMuted, map[string]any{"ids": items})
}

// findTasks returns live tasks whose title holds every word of query,
// newest first, reading at most taskSearchMaxPages pages.
func (b *bot) findTasks(ctx context.Context, query string) ([]pluginsdk.Task, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	var found []pluginsdk.Task
	page := pluginsdk.Page{Limit: taskSearchPageSize}
	for i := 0; i < taskSearchMaxPages; i++ {
		tasks, info, err := b.host.Tasks().List(ctx, pluginsdk.TaskFilter{}, page)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			if t.IsEphemeral {
				continue
			}
			title := strings.ToLower(t.Title)
			match := true
			for _, w := range words {
				if !strings.Contains(title, w) {
					match = false
					break
				}
			}
			if match {
				found = append(found, t)
			}
		}
		if info == nil || !info.HasMore || info.NextCursor == "" {
			break
		}
		page.Cursor = info.NextCursor
	}
	return found, nil
}

const (
	taskUsage   = "Usage: /task <words from the title>."
	muteUsage   = "Usage: /mute <words from the title>. You can also press Mute on an alert."
	noTaskMatch = "No task matches that. Titles are matched word by word."
)

// taskCommand answers /task: one match in full, or a short list.
func (b *bot) taskCommand(ctx context.Context, query string) outMessage {
	if strings.TrimSpace(query) == "" {
		return outMessage{Text: taskUsage}
	}
	found, err := b.findTasks(ctx, query)
	if err != nil {
		return outMessage{Text: "Could not read tasks: " + truncateRunes(err.Error(), maxReasonRunes)}
	}
	switch len(found) {
	case 0:
		return outMessage{Text: noTaskMatch}
	case 1:
		return b.describeTask(ctx, found[0])
	default:
		return taskList(found, "Several tasks match. Add words to narrow it down:", b.cfg.KandevURL)
	}
}

func (b *bot) describeTask(ctx context.Context, t pluginsdk.Task) outMessage {
	lines := []string{truncateRunes(taskLabel(&t), maxTitleRunes)}
	workflow, column := b.taskPlace(ctx, t)
	if workflow != "" {
		lines = append(lines, "Workflow: "+workflow)
	}
	if column != "" {
		lines = append(lines, "Column: "+column)
	}
	if t.State != "" {
		lines = append(lines, "State: "+t.State)
	}
	if updated, err := time.Parse(time.RFC3339Nano, t.UpdatedAt); err == nil {
		lines = append(lines, "Updated: "+updated.UTC().Format("2006-01-02 15:04 UTC"))
	}
	kind := alertKind("")
	if t.State == "COMPLETED" {
		kind = alertCompleted
	}
	buttons := append([]linkButton{{Text: "Open task", URL: taskURL(b.cfg.KandevURL, t.ID)}}, taskActions(t.ID, kind)...)
	return outMessage{Text: capMessage(strings.Join(lines, "\n")), Buttons: buttons}
}

// taskPlace names the task's workflow and column; empty when unreadable.
func (b *bot) taskPlace(ctx context.Context, t pluginsdk.Task) (workflow, column string) {
	if t.WorkflowID == "" {
		return "", ""
	}
	if workflows, _, err := b.host.Workflows().List(ctx, t.WorkspaceID, pluginsdk.Page{Limit: topologyPageSize}); err == nil {
		for _, wf := range workflows {
			if wf.ID == t.WorkflowID {
				workflow = wf.Name
			}
		}
	}
	if steps, err := b.host.Workflows().ListSteps(ctx, t.WorkflowID); err == nil {
		for _, s := range steps {
			if s.ID == t.WorkflowStepID {
				column = s.Name
			}
		}
	}
	return workflow, column
}

func taskList(tasks []pluginsdk.Task, heading, base string) outMessage {
	lines := []string{heading}
	var buttons []linkButton
	for i, t := range tasks {
		if i == maxListedTasks {
			lines = append(lines, fmt.Sprintf("and %d more", len(tasks)-maxListedTasks))
			break
		}
		line := "- " + truncateRunes(taskLabel(&t), maxTitleRunes)
		if t.State != "" {
			line += " (" + t.State + ")"
		}
		lines = append(lines, line)
		buttons = append(buttons, linkButton{Text: truncateRunes(taskLabel(&t), maxButtonTextRunes), URL: taskURL(base, t.ID)})
	}
	return outMessage{Text: capMessage(strings.Join(lines, "\n")), Buttons: buttons}
}

// muteCommand answers /mute <words>: exactly one match is muted.
func (b *bot) muteCommand(ctx context.Context, query string) outMessage {
	if strings.TrimSpace(query) == "" {
		return outMessage{Text: muteUsage}
	}
	found, err := b.findTasks(ctx, query)
	if err != nil {
		return outMessage{Text: "Could not read tasks: " + truncateRunes(err.Error(), maxReasonRunes)}
	}
	switch len(found) {
	case 0:
		return outMessage{Text: noTaskMatch}
	case 1:
		title := truncateRunes(taskLabel(&found[0]), maxTitleRunes)
		added, err := b.mute(ctx, found[0].ID)
		switch {
		case err != nil:
			return outMessage{Text: "Could not mute it: " + truncateRunes(err.Error(), maxReasonRunes)}
		case !added:
			return outMessage{Text: title + " is already muted."}
		default:
			return outMessage{Text: "Muted " + title + ". No alerts for it until you send /unmute."}
		}
	default:
		return taskList(found, "Several tasks match. Add words so only one does:", b.cfg.KandevURL)
	}
}

// unmuteCommand answers /unmute: without words it lists the muted tasks;
// with words it unmutes the muted tasks whose titles match; "all" clears it.
func (b *bot) unmuteCommand(ctx context.Context, query string) outMessage {
	muted, err := b.store.mutedTasks(ctx)
	if err != nil {
		return outMessage{Text: "Could not read muted tasks: " + truncateRunes(err.Error(), maxReasonRunes)}
	}
	if len(muted) == 0 {
		return outMessage{Text: "No task is muted."}
	}
	tasks := b.mutedTaskDetails(ctx, muted)
	query = strings.TrimSpace(query)
	if query == "" {
		lines := []string{"Muted tasks. Send /unmute <words from the title>, or /unmute all:"}
		for i, t := range tasks {
			if i == maxListedTasks {
				lines = append(lines, fmt.Sprintf("and %d more", len(tasks)-maxListedTasks))
				break
			}
			lines = append(lines, "- "+truncateRunes(taskLabel(&t), maxTitleRunes))
		}
		return outMessage{Text: capMessage(strings.Join(lines, "\n"))}
	}

	var ids []string
	if strings.EqualFold(query, "all") {
		ids = muted
	} else {
		words := strings.Fields(strings.ToLower(query))
		for _, t := range tasks {
			title := strings.ToLower(t.Title)
			match := true
			for _, w := range words {
				if !strings.Contains(title, w) {
					match = false
					break
				}
			}
			if match {
				ids = append(ids, t.ID)
			}
		}
	}
	if len(ids) == 0 {
		return outMessage{Text: "No muted task matches that. Send /unmute to list them."}
	}
	if err := b.unmute(ctx, ids); err != nil {
		return outMessage{Text: "Could not unmute: " + truncateRunes(err.Error(), maxReasonRunes)}
	}
	if len(ids) == 1 {
		for _, t := range tasks {
			if t.ID == ids[0] {
				return outMessage{Text: "Unmuted " + truncateRunes(taskLabel(&t), maxTitleRunes) + "."}
			}
		}
	}
	return outMessage{Text: fmt.Sprintf("Unmuted %d tasks.", len(ids))}
}

// mutedTaskDetails reads each muted task; one that cannot be read is listed
// by id so it can still be unmuted with "all".
func (b *bot) mutedTaskDetails(ctx context.Context, muted mutedList) []pluginsdk.Task {
	tasks := make([]pluginsdk.Task, 0, len(muted))
	for _, id := range muted {
		if t, err := b.host.Tasks().Get(ctx, id); err == nil && t != nil {
			tasks = append(tasks, *t)
			continue
		}
		tasks = append(tasks, pluginsdk.Task{ID: id, Title: "Unknown task " + id})
	}
	return tasks
}

// botCommands is the command menu Telegram shows when typing "/".
var botCommands = []tgBotCommand{
	{Command: "new", Description: "File a task through the triage agent"},
	{Command: "status", Description: "Running tasks and agents waiting on you"},
	{Command: "task", Description: "Find a task by title"},
	{Command: "mute", Description: "Stop alerts for a task"},
	{Command: "unmute", Description: "List or unmute muted tasks"},
	{Command: "help", Description: "What the bot can do"},
}

// registerCommands sets the command menu. It is best effort.
func (b *bot) registerCommands(ctx context.Context) {
	if err := b.tg.SetCommands(ctx, botCommands); err != nil && ctx.Err() == nil {
		log.Printf("telegram: setting the command menu: %v", err)
	}
}
