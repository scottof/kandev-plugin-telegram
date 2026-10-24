package main

import (
	"context"
	"log"
)

const (
	eventTaskCreated = "task.created"
	eventTaskUpdated = "task.updated"

	stateKeyTaskSteps = "task_steps"
	// taskStepsCap bounds the last-seen column log kept in Host state.
	taskStepsCap = 500
)

// stepChange is one report of where a task stands, from any task event.
type stepChange struct {
	TaskID     string
	StepID     string
	WorkflowID string
	// FromStepID and FromWorkflowID are only known for task.moved.
	FromStepID     string
	FromWorkflowID string
	// Title, State and Ephemeral are known for task.created/updated; for
	// task.moved they are read from the task.
	Title      string
	State      string
	Ephemeral  bool
	knownTask  bool
	Transition string
	UpdatedAt  string
}

// stepChangeFromEvent reads a task event payload. ok is false for events that
// say nothing about a column.
func stepChangeFromEvent(eventType string, payload map[string]any) (stepChange, bool) {
	str := func(key string) string { v, _ := payload[key].(string); return v }
	c := stepChange{TaskID: str("task_id"), Transition: str("step_transition_id"), UpdatedAt: str("updated_at")}
	switch eventType {
	case eventTaskMoved:
		c.StepID, c.WorkflowID = str("to_step_id"), str("to_workflow_id")
		c.FromStepID, c.FromWorkflowID = str("from_step_id"), str("from_workflow_id")
		if c.Transition == "" {
			c.Transition = str("move_id")
		}
	case eventTaskCreated, eventTaskUpdated:
		c.StepID, c.WorkflowID = str("workflow_step_id"), str("workflow_id")
		c.Title, c.State = str("title"), str("state")
		c.Ephemeral, _ = payload["is_ephemeral"].(bool)
		c.knownTask = true
	default:
		return stepChange{}, false
	}
	return c, c.TaskID != "" && c.StepID != ""
}

// columnAlertFromEvent turns task events into opt-in column alerts. kandev
// publishes task.moved for board and API moves but only task.updated for
// the moves its workflows make, for example to Review after an agent turn.
// So the plugin keeps each task's last seen column and alerts when a later
// event reports a different one, whichever event arrives first; the other
// then finds the column already recorded. A task first seen through
// task.updated is only recorded: its earlier column is unknown.
func (b *bot) columnAlertFromEvent(ctx context.Context, eventType string, payload map[string]any) (alert, bool, error) {
	if !b.cfg.AlertColumnMoves {
		return alert{}, false, nil
	}
	c, ok := stepChangeFromEvent(eventType, payload)
	if !ok || c.Ephemeral {
		return alert{}, false, nil
	}

	b.stepsMu.Lock()
	defer b.stepsMu.Unlock()
	steps, err := b.store.taskSteps(ctx)
	if err != nil {
		return alert{}, false, err
	}
	previous := steps.get(c.TaskID)
	if previous == "" {
		previous = c.FromStepID
	}
	if previous == c.StepID {
		return alert{}, false, nil
	}

	var a alert
	queue := previous != "" && eventType != eventTaskCreated
	if queue {
		a, queue, err = b.columnAlert(ctx, c, previous)
		if err != nil {
			return alert{}, false, err
		}
	}
	// Queue before recording the column: if the record write fails, the
	// redelivered event is deduped by the alert marker instead of lost.
	if queue {
		if _, queued, err := b.queueTaskAlert(ctx, a, columnMarker(previous, c)); err != nil {
			return alert{}, false, err
		} else if !queued {
			queue = false
		}
	}
	if err := b.store.setTaskSteps(ctx, steps.set(c.TaskID, c.StepID)); err != nil {
		return alert{}, false, err
	}
	return a, queue, nil
}

// columnAlert builds the alert, or reports false when the move is left to
// another alert: a move that completed the task is the Finished alert's.
func (b *bot) columnAlert(ctx context.Context, c stepChange, previous string) (alert, bool, error) {
	if !c.knownTask {
		task, err := b.host.Tasks().Get(ctx, c.TaskID)
		if err != nil {
			return alert{}, false, err
		}
		if task == nil {
			return alert{}, false, nil
		}
		c.Title, c.State, c.Ephemeral, c.UpdatedAt = task.Title, task.State, task.IsEphemeral, task.UpdatedAt
	}
	if c.Ephemeral || (c.State == "COMPLETED" && b.cfg.AlertCompleted) {
		return alert{}, false, nil
	}
	title := c.Title
	if title == "" {
		title = untitledTask
	}
	names := b.stepNames(ctx, c.WorkflowID, c.FromWorkflowID)
	detail := columnName(names, previous) + " to " + columnName(names, c.StepID)
	return alert{Kind: alertMoved, TaskID: c.TaskID, TaskTitle: title, Detail: detail}, true, nil
}

// columnMarker dedupes redelivery of one move. kandev may send no transition
// id, so the task's update time stands in; a later identical move then still
// alerts.
func columnMarker(previous string, c stepChange) string {
	at := c.Transition
	if at == "" {
		at = c.UpdatedAt
	}
	return "moved:" + previous + ">" + c.StepID + "@" + at
}

// stepNames maps step ids to column names for the given workflows. A
// workflow whose steps cannot be read is skipped; its columns then show as
// unknown rather than failing the alert.
func (b *bot) stepNames(ctx context.Context, workflowIDs ...string) map[string]string {
	names := map[string]string{}
	seen := map[string]bool{}
	for _, id := range workflowIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		steps, err := b.host.Workflows().ListSteps(ctx, id)
		if err != nil {
			log.Printf("telegram: reading columns of a workflow: %v", err)
			continue
		}
		for _, s := range steps {
			names[s.ID] = s.Name
		}
	}
	return names
}

func columnName(names map[string]string, stepID string) string {
	if name := names[stepID]; name != "" {
		return name
	}
	return "another column"
}

// taskStepLog is each task's last seen column, oldest first, capped.
type taskStepLog []taskStepEntry

type taskStepEntry struct {
	TaskID string
	StepID string
}

func (l taskStepLog) get(taskID string) string {
	for _, e := range l {
		if e.TaskID == taskID {
			return e.StepID
		}
	}
	return ""
}

// set records a column, moving the task to the newest end and dropping the
// oldest entries past the cap.
func (l taskStepLog) set(taskID, stepID string) taskStepLog {
	out := make(taskStepLog, 0, len(l)+1)
	for _, e := range l {
		if e.TaskID != taskID {
			out = append(out, e)
		}
	}
	out = append(out, taskStepEntry{TaskID: taskID, StepID: stepID})
	if len(out) > taskStepsCap {
		out = out[len(out)-taskStepsCap:]
	}
	return out
}

func (s stateStore) taskSteps(ctx context.Context) (taskStepLog, error) {
	value, err := s.get(ctx, stateKeyTaskSteps)
	if err != nil {
		return nil, err
	}
	var steps taskStepLog
	items, _ := value["entries"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		taskID, _ := item["task_id"].(string)
		stepID, _ := item["step_id"].(string)
		if taskID != "" && stepID != "" {
			steps = append(steps, taskStepEntry{TaskID: taskID, StepID: stepID})
		}
	}
	return steps, nil
}

func (s stateStore) setTaskSteps(ctx context.Context, steps taskStepLog) error {
	items := make([]any, 0, len(steps))
	for _, e := range steps {
		items = append(items, map[string]any{"task_id": e.TaskID, "step_id": e.StepID})
	}
	return s.set(ctx, stateKeyTaskSteps, map[string]any{"entries": items})
}
