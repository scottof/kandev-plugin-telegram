package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	stateScope = "instance"

	stateKeyOffset           = "telegram_offset"
	stateKeySentInteractions = "sent_interactions"
	stateKeyTaskAlerts       = "task_alerts"
	stateKeyBotStatus        = "bot_status"

	// taskAlertLogCap bounds the task alert dedupe log kept in Host state.
	taskAlertLogCap = 200
)

// stateStore wraps Host state with typed accessors. Host state values are
// JSON objects, and numbers come back as float64.
type stateStore struct {
	host pluginsdk.Host
}

func (s stateStore) get(ctx context.Context, key string) (map[string]any, error) {
	value, _, err := s.host.GetState(ctx, stateScope, "", key)
	if err != nil {
		return nil, fmt.Errorf("reading state %s: %w", key, err)
	}
	return value, nil
}

func (s stateStore) set(ctx context.Context, key string, value map[string]any) error {
	if err := s.host.SetState(ctx, stateScope, "", key, value); err != nil {
		return fmt.Errorf("writing state %s: %w", key, err)
	}
	return nil
}

// offset is the next Telegram update_id to request from getUpdates.
func (s stateStore) offset(ctx context.Context) (int64, error) {
	value, err := s.get(ctx, stateKeyOffset)
	if err != nil {
		return 0, err
	}
	n, _ := value["offset"].(float64)
	return int64(n), nil
}

func (s stateStore) setOffset(ctx context.Context, offset int64) error {
	return s.set(ctx, stateKeyOffset, map[string]any{"offset": offset})
}

// sentInteractions is the set of pending interaction ids already alerted.
func (s stateStore) sentInteractions(ctx context.Context) (map[string]bool, error) {
	value, err := s.get(ctx, stateKeySentInteractions)
	if err != nil {
		return nil, err
	}
	sent := map[string]bool{}
	ids, _ := value["ids"].([]any)
	for _, id := range ids {
		if s, ok := id.(string); ok {
			sent[s] = true
		}
	}
	return sent, nil
}

func (s stateStore) setSentInteractions(ctx context.Context, sent map[string]bool) error {
	sorted := make([]string, 0, len(sent))
	for id := range sent {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	// Host state goes through structpb, which accepts []any but not []string.
	ids := make([]any, len(sorted))
	for i, id := range sorted {
		ids[i] = id
	}
	return s.set(ctx, stateKeySentInteractions, map[string]any{"ids": ids})
}

// taskAlertEntry records the last alerted transition of one task. Marker is
// "<new_state>@<updated_at>", so a redelivered event matches and is skipped.
type taskAlertEntry struct {
	TaskID string
	Marker string
}

// taskAlertLog is ordered oldest first and capped at taskAlertLogCap.
type taskAlertLog []taskAlertEntry

func (l taskAlertLog) seen(taskID, marker string) bool {
	for _, e := range l {
		if e.TaskID == taskID && e.Marker == marker {
			return true
		}
	}
	return false
}

func (l taskAlertLog) add(taskID, marker string) taskAlertLog {
	next := make(taskAlertLog, 0, len(l)+1)
	for _, e := range l {
		if e.TaskID != taskID {
			next = append(next, e)
		}
	}
	next = append(next, taskAlertEntry{TaskID: taskID, Marker: marker})
	if len(next) > taskAlertLogCap {
		next = next[len(next)-taskAlertLogCap:]
	}
	return next
}

func (s stateStore) taskAlerts(ctx context.Context) (taskAlertLog, error) {
	value, err := s.get(ctx, stateKeyTaskAlerts)
	if err != nil {
		return nil, err
	}
	var log taskAlertLog
	entries, _ := value["entries"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		taskID, _ := entry["task_id"].(string)
		marker, _ := entry["marker"].(string)
		if taskID != "" {
			log = append(log, taskAlertEntry{TaskID: taskID, Marker: marker})
		}
	}
	return log, nil
}

func (s stateStore) setTaskAlerts(ctx context.Context, log taskAlertLog) error {
	entries := make([]any, 0, len(log))
	for _, e := range log {
		entries = append(entries, map[string]any{"task_id": e.TaskID, "marker": e.Marker})
	}
	return s.set(ctx, stateKeyTaskAlerts, map[string]any{"entries": entries})
}

// botStatus is what the settings card shows. It never holds the token.
type botStatus struct {
	Connected    bool   `json:"connected"`
	Username     string `json:"username"`
	LastUpdateAt string `json:"last_update_at"`
	LastError    string `json:"last_error"`
}

func (s stateStore) botStatus(ctx context.Context) (botStatus, error) {
	value, err := s.get(ctx, stateKeyBotStatus)
	if err != nil {
		return botStatus{}, err
	}
	var st botStatus
	st.Connected, _ = value["connected"].(bool)
	st.Username, _ = value["username"].(string)
	st.LastUpdateAt, _ = value["last_update_at"].(string)
	st.LastError, _ = value["last_error"].(string)
	return st, nil
}

func (s stateStore) setBotStatus(ctx context.Context, st botStatus) error {
	return s.set(ctx, stateKeyBotStatus, map[string]any{
		"connected":      st.Connected,
		"username":       st.Username,
		"last_update_at": st.LastUpdateAt,
		"last_error":     st.LastError,
	})
}
