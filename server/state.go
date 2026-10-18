package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	stateScope = "instance"

	stateKeyOffset           = "telegram_offset"
	stateKeySentInteractions = "sent_interactions"
	stateKeyTaskAlerts       = "task_alerts"
	stateKeyTaskAlertQueue   = "task_alert_queue"
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

// sentSet holds, per allowed user, the pending interaction ids whose alert is
// settled for that user: delivered, or refused by Telegram for good.
type sentSet map[int64]map[string]bool

func (s sentSet) has(userID int64, id string) bool { return s[userID][id] }

func (s sentSet) add(userID int64, id string) {
	if s[userID] == nil {
		s[userID] = map[string]bool{}
	}
	s[userID][id] = true
}

func (s sentSet) equal(other sentSet) bool {
	if len(s) != len(other) {
		return false
	}
	for userID, ids := range s {
		if !sameSet(ids, other[userID]) {
			return false
		}
	}
	return true
}

// sentInteractions reads the sent set. Struct keys must be strings, so user
// ids are stored in decimal.
func (s stateStore) sentInteractions(ctx context.Context) (sentSet, error) {
	value, err := s.get(ctx, stateKeySentInteractions)
	if err != nil {
		return nil, err
	}
	sent := sentSet{}
	users, _ := value["users"].(map[string]any)
	for key, raw := range users {
		userID, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			continue
		}
		ids, _ := raw.([]any)
		for _, id := range ids {
			if s, ok := id.(string); ok {
				sent.add(userID, s)
			}
		}
	}
	return sent, nil
}

func (s stateStore) setSentInteractions(ctx context.Context, sent sentSet) error {
	users := make(map[string]any, len(sent))
	for userID, set := range sent {
		if len(set) == 0 {
			continue
		}
		sorted := make([]string, 0, len(set))
		for id := range set {
			sorted = append(sorted, id)
		}
		sort.Strings(sorted)
		// Host state goes through structpb, which accepts []any but not []string.
		ids := make([]any, len(sorted))
		for i, id := range sorted {
			ids[i] = id
		}
		users[strconv.FormatInt(userID, 10)] = ids
	}
	return s.set(ctx, stateKeySentInteractions, map[string]any{"users": users})
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

// queuedTaskAlert is a task alert waiting until every allowed user is
// settled. Marker is the same value as its task alert log entry, so a
// redelivered event queues once. SettledFor lists the users already settled.
type queuedTaskAlert struct {
	alert
	Marker     string
	SettledFor []int64
}

// taskAlertKey identifies a queued task alert.
type taskAlertKey struct {
	TaskID string
	Marker string
}

func (q queuedTaskAlert) key() taskAlertKey { return taskAlertKey{TaskID: q.TaskID, Marker: q.Marker} }

func (q queuedTaskAlert) settledFor(userID int64) bool {
	for _, id := range q.SettledFor {
		if id == userID {
			return true
		}
	}
	return false
}

func (s stateStore) taskAlertQueue(ctx context.Context) ([]queuedTaskAlert, error) {
	value, err := s.get(ctx, stateKeyTaskAlertQueue)
	if err != nil {
		return nil, err
	}
	var queue []queuedTaskAlert
	items, _ := value["alerts"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		var q queuedTaskAlert
		kind, _ := item["kind"].(string)
		q.Kind = alertKind(kind)
		q.TaskID, _ = item["task_id"].(string)
		q.TaskTitle, _ = item["title"].(string)
		q.Detail, _ = item["detail"].(string)
		q.Marker, _ = item["marker"].(string)
		settled, _ := item["settled_for"].([]any)
		for _, raw := range settled {
			if n, ok := raw.(float64); ok {
				q.SettledFor = append(q.SettledFor, int64(n))
			}
		}
		if q.TaskID != "" {
			queue = append(queue, q)
		}
	}
	return queue, nil
}

func (s stateStore) setTaskAlertQueue(ctx context.Context, queue []queuedTaskAlert) error {
	items := make([]any, 0, len(queue))
	for _, q := range queue {
		settled := make([]any, len(q.SettledFor))
		for i, id := range q.SettledFor {
			settled[i] = id
		}
		items = append(items, map[string]any{
			"kind": string(q.Kind), "task_id": q.TaskID, "title": q.TaskTitle, "detail": q.Detail, "marker": q.Marker,
			"settled_for": settled,
		})
	}
	return s.set(ctx, stateKeyTaskAlertQueue, map[string]any{"alerts": items})
}

// botStatus is what the settings card shows. It never holds the token.
type botStatus struct {
	Connected    bool   `json:"connected"`
	Username     string `json:"username"`
	LastUpdateAt string `json:"last_update_at"`
	LastError    string `json:"last_error"`
	// AlertError is the last alert Telegram refused for good. It is kept
	// apart from LastError, which every successful poll clears.
	AlertError string `json:"alert_error"`
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
	st.AlertError, _ = value["alert_error"].(string)
	return st, nil
}

func (s stateStore) setBotStatus(ctx context.Context, st botStatus) error {
	return s.set(ctx, stateKeyBotStatus, map[string]any{
		"connected":      st.Connected,
		"username":       st.Username,
		"last_update_at": st.LastUpdateAt,
		"last_error":     st.LastError,
		"alert_error":    st.AlertError,
	})
}
