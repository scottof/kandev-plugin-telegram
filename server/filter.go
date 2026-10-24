package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embedded so the timezone setting works on hosts without zoneinfo files.
	_ "time/tzdata"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const defaultTimezone = "Europe/Amsterdam"

// quietHours is a daily window, in minutes after local midnight, during which
// alerts wait. End before start means the window crosses midnight.
type quietHours struct {
	start, end int
	loc        *time.Location
}

var errQuietHours = errors.New("quiet_hours: use HH:MM-HH:MM, for example 22:00-07:00")

// parseQuietHours reads "HH:MM-HH:MM" in timezone; an empty value is off.
func parseQuietHours(value, timezone string) (*quietHours, error) {
	if timezone == "" {
		timezone = defaultTimezone
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone: %q is not an IANA timezone such as Europe/Amsterdam", timezone)
	}
	if value == "" {
		return nil, nil
	}
	from, to, ok := strings.Cut(value, "-")
	if !ok {
		return nil, errQuietHours
	}
	start, okStart := parseClock(from)
	end, okEnd := parseClock(to)
	if !okStart || !okEnd || start == end {
		return nil, errQuietHours
	}
	return &quietHours{start: start, end: end, loc: loc}, nil
}

func parseClock(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hour, errH := strconv.Atoi(h)
	minute, errM := strconv.Atoi(m)
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 || len(m) != 2 {
		return 0, false
	}
	return hour*60 + minute, true
}

// active reports whether now falls inside the window.
func (q *quietHours) active(now time.Time) bool {
	if q == nil {
		return false
	}
	local := now.In(q.loc)
	m := local.Hour()*60 + local.Minute()
	if q.start < q.end {
		return m >= q.start && m < q.end
	}
	return m >= q.start || m < q.end
}

// parseWorkflowFilter reads comma-separated workflow names, lowercased.
func parseWorkflowFilter(value string) []string {
	var names []string
	for _, part := range strings.Split(value, ",") {
		if name := strings.ToLower(strings.TrimSpace(part)); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// alertFilter decides per flush which alerts are dropped: those of muted
// tasks and, with a workflow filter set, of other workflows. A task or
// workflow that cannot be read is not dropped.
type alertFilter struct {
	b       *bot
	muted   mutedList
	tasks   map[string]*pluginsdk.Task
	allowed map[string]bool
}

func (b *bot) newAlertFilter(ctx context.Context) *alertFilter {
	f := &alertFilter{b: b, tasks: map[string]*pluginsdk.Task{}, allowed: map[string]bool{}}
	if muted, err := b.store.mutedTasks(ctx); err == nil {
		f.muted = muted
	}
	return f
}

// task reads a task once per flush; nil when it cannot be read.
func (f *alertFilter) task(ctx context.Context, taskID string) *pluginsdk.Task {
	if t, ok := f.tasks[taskID]; ok {
		return t
	}
	t, err := f.b.host.Tasks().Get(ctx, taskID)
	if err != nil {
		t = nil
	}
	f.tasks[taskID] = t
	return t
}

func (f *alertFilter) dropped(ctx context.Context, taskID, workflowID string) bool {
	if f.muted.has(taskID) {
		return true
	}
	if len(f.b.cfg.AlertWorkflows) == 0 {
		return false
	}
	if workflowID == "" {
		t := f.task(ctx, taskID)
		if t == nil {
			return false
		}
		workflowID = t.WorkflowID
	}
	if ok, seen := f.allowed[workflowID]; seen {
		return !ok
	}
	name, known := f.b.workflowName(ctx, workflowID)
	ok := !known || containsName(f.b.cfg.AlertWorkflows, name)
	f.allowed[workflowID] = ok
	return !ok
}

func containsName(names []string, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// workflowName looks a workflow up in a cache, refreshing it from every
// workspace once when the id is unknown. known is false when it stays unknown.
func (b *bot) workflowName(ctx context.Context, workflowID string) (string, bool) {
	if name, ok := b.workflowNames[workflowID]; ok {
		return name, true
	}
	names := map[string]string{}
	workspaces, _, err := b.host.Workspaces().List(ctx, pluginsdk.Page{Limit: topologyPageSize})
	if err != nil {
		return "", false
	}
	for _, ws := range workspaces {
		workflows, _, err := b.host.Workflows().List(ctx, ws.ID, pluginsdk.Page{Limit: topologyPageSize})
		if err != nil {
			continue
		}
		for _, wf := range workflows {
			names[wf.ID] = wf.Name
		}
	}
	b.workflowNames = names
	name, ok := names[workflowID]
	return name, ok
}
