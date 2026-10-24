package main

import (
	"context"
	"log"
)

const stateKeyInflight = "inflight_command"

// inflightCommand is the command being handled. The update offset is stored
// before a command runs, so a restart mid-command drops it rather than
// replaying it; this record lets the next start tell the sender instead of
// going silent.
type inflightCommand struct {
	ChatID    int64
	MessageID int64
	Command   string
	// TaskID is set once /new has created its task.
	TaskID string
}

// beginCommand records msg as in flight. It is best effort: a failed write
// only costs the notice after a restart.
func (b *bot) beginCommand(ctx context.Context, msg *tgMessage, command string) {
	rec := map[string]any{"chat_id": msg.Chat.ID, "message_id": msg.MessageID, "command": command, "task_id": ""}
	if err := b.store.set(ctx, stateKeyInflight, rec); err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}
}

// endCommand clears the record once the reply has been sent.
func (b *bot) endCommand(ctx context.Context) {
	if err := b.store.set(ctx, stateKeyInflight, map[string]any{}); err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}
}

// markInflightTask notes that /new created taskID, so a restart before the
// reply can still point to the task.
func (b *bot) markInflightTask(ctx context.Context, taskID string) {
	rec, ok, err := b.inflight(ctx)
	if err != nil || !ok {
		return
	}
	value := map[string]any{"chat_id": rec.ChatID, "message_id": rec.MessageID, "command": rec.Command, "task_id": taskID}
	if err := b.store.set(ctx, stateKeyInflight, value); err != nil && ctx.Err() == nil {
		log.Printf("telegram: %v", err)
	}
}

func (b *bot) inflight(ctx context.Context) (inflightCommand, bool, error) {
	value, err := b.store.get(ctx, stateKeyInflight)
	if err != nil {
		return inflightCommand{}, false, err
	}
	chatID, _ := value["chat_id"].(float64)
	messageID, _ := value["message_id"].(float64)
	command, _ := value["command"].(string)
	taskID, _ := value["task_id"].(string)
	if chatID == 0 || command == "" {
		return inflightCommand{}, false, nil
	}
	return inflightCommand{ChatID: int64(chatID), MessageID: int64(messageID), Command: command, TaskID: taskID}, true, nil
}

// reportInterrupted tells the sender of a command a restart cut short, then
// clears the record so the notice is sent once.
func (b *bot) reportInterrupted(ctx context.Context) {
	rec, ok, err := b.inflight(ctx)
	if err != nil {
		log.Printf("telegram: %v", err)
		return
	}
	if !ok {
		return
	}
	out := outMessage{Text: "A plugin restart interrupted your /" + rec.Command + ". Send it again."}
	if rec.Command == "new" {
		out.Text = "A plugin restart interrupted your /new before a task was created. Send it again."
		if rec.TaskID != "" {
			title := untitledTask
			if task, err := b.host.Tasks().Get(ctx, rec.TaskID); err == nil && task != nil && task.Title != "" {
				title = task.Title
			}
			out = outMessage{
				Text:    "A plugin restart interrupted your /new after the task was created, so do not send it again.\n" + truncateRunes(title, maxTitleRunes),
				Buttons: []linkButton{{Text: "Open task", URL: taskURL(b.cfg.KandevURL, rec.TaskID)}},
			}
		}
	}
	if _, err := b.sendWithFallback(ctx, rec.ChatID, out, rec.MessageID); err != nil && ctx.Err() == nil {
		log.Printf("telegram: reporting an interrupted command: %v", err)
	}
	b.endCommand(ctx)
}
