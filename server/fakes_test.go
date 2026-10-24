package main

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeHost is an in-memory pluginsdk.Host. SetState round-trips values through
// a protobuf Struct like the real Host.
type fakeHost struct {
	pluginsdk.UnimplementedHostData
	mu     sync.Mutex
	state  map[string]map[string]any
	config map[string]any
	d      *fakeData
	// setStateErr makes SetState fail for the given key.
	setStateErr map[string]error
	// getStateFailures makes GetState fail that many times for the given key.
	getStateFailures map[string]int
}

func newFakeHost() *fakeHost {
	return &fakeHost{state: make(map[string]map[string]any), config: map[string]any{}, d: &fakeData{}}
}

func fakeStateKey(scope, scopeID, key string) string {
	return scope + "/" + scopeID + "/" + key
}

func (h *fakeHost) GetState(_ context.Context, scope, scopeID, key string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.getStateFailures[key] > 0 {
		h.getStateFailures[key]--
		return nil, false, errors.New("state unavailable")
	}
	v, ok := h.state[fakeStateKey(scope, scopeID, key)]
	return v, ok, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, scopeID, key string, value map[string]any) error {
	stored, err := structRoundTrip(value)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.setStateErr[key]; err != nil {
		return err
	}
	h.state[fakeStateKey(scope, scopeID, key)] = stored
	return nil
}

func (h *fakeHost) DeleteState(_ context.Context, scope, scopeID, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, fakeStateKey(scope, scopeID, key))
	return nil
}

func (h *fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}

func (h *fakeHost) GetConfig(context.Context) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.config, nil
}

func (h *fakeHost) RevealSecret(context.Context, string) (string, error) { return "", nil }
func (h *fakeHost) GetSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (h *fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (h *fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (h *fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

var _ pluginsdk.Host = (*fakeHost)(nil)

// fakeData holds the Host data API fixtures behind their own mutex.
type fakeData struct {
	mu             sync.Mutex
	tasks          []pluginsdk.Task
	pending        []pluginsdk.Interaction
	listPendingErr error
	listCalls      int
	listTasksErr   error
	lastTaskFilter pluginsdk.TaskFilter

	workspaces   []pluginsdk.Workspace
	workflows    []pluginsdk.Workflow
	steps        map[string][]pluginsdk.WorkflowStep
	repositories []pluginsdk.Repository
	created      []pluginsdk.CreateTaskInput
	createErr    error
	// createdState is the state Create reports, as a step that completes
	// tasks would.
	createdState string
	// onCreate runs inside Create, with the data lock held.
	onCreate func()
	moves    []pluginsdk.MoveTaskInput
	moveErr  error

	agentReply   string
	agentErr     error
	agentPrompts []string
	agentOptions []pluginsdk.UtilityAgentOptions
	// agentErrByProfile fails calls for one profile id ("" is a call without
	// a profile, which kandev resolves to its utility default).
	agentErrByProfile map[string]error
	agentProfileCalls []string
	// onAgentInvoke runs inside InvokeUtilityAgent, with the data lock held.
	onAgentInvoke func()
}

func (h *fakeHost) data() *fakeData { return h.d }

func (h *fakeHost) addTask(task pluginsdk.Task) {
	d := h.data()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tasks = append(d.tasks, task)
}

func (h *fakeHost) setPending(items ...pluginsdk.Interaction) {
	d := h.data()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = items
}

func (h *fakeHost) Tasks() pluginsdk.TaskReader { return &fakeTasks{d: h.data()} }

func (h *fakeHost) Workspaces() pluginsdk.WorkspaceReader { return &fakeWorkspaces{d: h.data()} }

func (h *fakeHost) Workflows() pluginsdk.WorkflowReader { return &fakeWorkflows{d: h.data()} }

func (h *fakeHost) Repositories() pluginsdk.RepositoryReader {
	return &fakeRepositories{d: h.data()}
}

func (h *fakeHost) InvokeUtilityAgent(_ context.Context, prompt string, options ...pluginsdk.UtilityAgentOptions) (string, error) {
	d := h.data()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agentPrompts = append(d.agentPrompts, prompt)
	d.agentOptions = append(d.agentOptions, options...)
	profileID := ""
	if len(options) > 0 {
		profileID = options[0].ProfileID
	}
	d.agentProfileCalls = append(d.agentProfileCalls, profileID)
	if d.onAgentInvoke != nil {
		d.onAgentInvoke()
	}
	if err, ok := d.agentErrByProfile[profileID]; ok {
		return "", err
	}
	return d.agentReply, d.agentErr
}

type fakeWorkspaces struct {
	pluginsdk.WorkspaceReader
	d *fakeData
}

func (f *fakeWorkspaces) List(context.Context, pluginsdk.Page) ([]pluginsdk.Workspace, *pluginsdk.PageInfo, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return append([]pluginsdk.Workspace(nil), f.d.workspaces...), &pluginsdk.PageInfo{}, nil
}

type fakeWorkflows struct {
	pluginsdk.WorkflowReader
	d *fakeData
}

func (f *fakeWorkflows) List(_ context.Context, workspaceID string, _ pluginsdk.Page) ([]pluginsdk.Workflow, *pluginsdk.PageInfo, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	var out []pluginsdk.Workflow
	for _, wf := range f.d.workflows {
		if wf.WorkspaceID == workspaceID {
			out = append(out, wf)
		}
	}
	return out, &pluginsdk.PageInfo{}, nil
}

func (f *fakeWorkflows) ListSteps(_ context.Context, workflowID string) ([]pluginsdk.WorkflowStep, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return append([]pluginsdk.WorkflowStep(nil), f.d.steps[workflowID]...), nil
}

type fakeRepositories struct {
	pluginsdk.RepositoryReader
	d *fakeData
}

func (f *fakeRepositories) List(_ context.Context, workspaceID string, _ pluginsdk.Page) ([]pluginsdk.Repository, *pluginsdk.PageInfo, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	var out []pluginsdk.Repository
	for _, r := range f.d.repositories {
		if r.WorkspaceID == workspaceID {
			out = append(out, r)
		}
	}
	return out, &pluginsdk.PageInfo{}, nil
}

func (h *fakeHost) Interactions() pluginsdk.InteractionAccessor {
	return &fakeInteractions{d: h.data()}
}

type fakeTasks struct {
	pluginsdk.TaskReader
	d *fakeData
}

func (f *fakeTasks) Get(_ context.Context, id string) (*pluginsdk.Task, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	for _, t := range f.d.tasks {
		if t.ID == id {
			t := t
			return &t, nil
		}
	}
	return nil, errors.New("task not found")
}

// List filters by state only and returns tasks newest first, like the host.
func (f *fakeTasks) List(_ context.Context, filter pluginsdk.TaskFilter, page pluginsdk.Page) ([]pluginsdk.Task, *pluginsdk.PageInfo, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	f.d.lastTaskFilter = filter
	if f.d.listTasksErr != nil {
		return nil, nil, f.d.listTasksErr
	}
	var out []pluginsdk.Task
	for _, t := range f.d.tasks {
		if len(filter.States) == 0 || slices.Contains(filter.States, t.State) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	if page.Limit > 0 && len(out) > int(page.Limit) {
		out = out[:page.Limit]
	}
	return out, &pluginsdk.PageInfo{}, nil
}

func (f *fakeTasks) Create(_ context.Context, in pluginsdk.CreateTaskInput) (*pluginsdk.Task, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.d.createErr != nil {
		return nil, f.d.createErr
	}
	f.d.created = append(f.d.created, in)
	task := pluginsdk.Task{ID: "task-new", Identifier: "KAN-7", Title: in.Title, WorkspaceID: in.WorkspaceID, WorkflowID: in.WorkflowID, State: f.d.createdState}
	f.d.tasks = append(f.d.tasks, task)
	if f.d.onCreate != nil {
		f.d.onCreate()
	}
	return &task, nil
}

func (f *fakeTasks) Move(_ context.Context, in pluginsdk.MoveTaskInput) (*pluginsdk.MoveTaskOutcome, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.d.moveErr != nil {
		return nil, f.d.moveErr
	}
	f.d.moves = append(f.d.moves, in)
	return &pluginsdk.MoveTaskOutcome{Transitioned: true}, nil
}

type fakeInteractions struct {
	pluginsdk.InteractionAccessor
	d *fakeData
}

// ListPending pages with the cursor holding the next index.
func (f *fakeInteractions) ListPending(_ context.Context, _ pluginsdk.InteractionFilter, page pluginsdk.Page) ([]pluginsdk.Interaction, *pluginsdk.PageInfo, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	f.d.listCalls++
	if f.d.listPendingErr != nil {
		return nil, nil, f.d.listPendingErr
	}
	start := 0
	if page.Cursor != "" {
		start, _ = strconv.Atoi(page.Cursor)
	}
	limit := int(page.Limit)
	if limit <= 0 {
		limit = len(f.d.pending)
	}
	end := start + limit
	if end > len(f.d.pending) {
		end = len(f.d.pending)
	}
	info := &pluginsdk.PageInfo{}
	if end < len(f.d.pending) {
		info.HasMore = true
		info.NextCursor = strconv.Itoa(end)
	}
	return append([]pluginsdk.Interaction(nil), f.d.pending[start:end]...), info, nil
}

// fakeClock records sleeps instead of waiting, and advances Now by each sleep.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	slept   []time.Duration
	onSleep func()
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	hook := c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ctx.Err()
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// fakeTelegram is an in-memory telegramClient. GetUpdates serves queued
// results in order; once the queue is empty it calls onDrained and then blocks
// like a long poll until the context ends.
type fakeTelegram struct {
	mu        sync.Mutex
	me        tgUser
	meErr     error
	queue     []fakeUpdatesResult
	polled    []int64
	sent      []sentMessage
	sendErr   error
	sendErrTo map[int64]error
	// sendErrWithButtons fails only messages that carry URL buttons.
	sendErrWithButtons error
	attempts           int
	attemptsTo         map[int64]int
	hold               time.Duration
	onSend             func()
	onDrained          func()
	reactions          []fakeReaction
	reactionErr        error
}

type fakeReaction struct {
	ChatID, MessageID int64
	Emoji             string
}

type fakeUpdatesResult struct {
	updates []tgUpdate
	err     error
}

type sentMessage struct {
	ChatID  int64
	Msg     outMessage
	ReplyTo int64
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{me: tgUser{ID: 99, IsBot: true, FirstName: "Kandev", Username: "kandev_bot"}}
}

func (f *fakeTelegram) queueUpdates(updates ...tgUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, fakeUpdatesResult{updates: updates})
}

func (f *fakeTelegram) queueError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, fakeUpdatesResult{err: err})
}

func (f *fakeTelegram) GetMe(context.Context) (tgUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.me, f.meErr
}

func (f *fakeTelegram) GetUpdates(ctx context.Context, offset int64, _ int) ([]tgUpdate, error) {
	f.mu.Lock()
	f.polled = append(f.polled, offset)
	if len(f.queue) > 0 {
		next := f.queue[0]
		f.queue = f.queue[1:]
		f.mu.Unlock()
		return next.updates, next.err
	}
	drained := f.onDrained
	f.mu.Unlock()
	if drained != nil {
		drained()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeTelegram) SendMessage(_ context.Context, chatID int64, msg outMessage, replyTo int64) error {
	f.mu.Lock()
	hook := f.onSend
	f.attempts++
	if f.attemptsTo == nil {
		f.attemptsTo = map[int64]int{}
	}
	f.attemptsTo[chatID]++
	err := f.sendErr
	if e, ok := f.sendErrTo[chatID]; ok {
		err = e
	}
	if err == nil && len(msg.Buttons) > 0 {
		err = f.sendErrWithButtons
	}
	if err == nil {
		f.sent = append(f.sent, sentMessage{ChatID: chatID, Msg: msg, ReplyTo: replyTo})
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeTelegram) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeTelegram) sendAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeTelegram) sendAttemptsTo(chatID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attemptsTo[chatID]
}

func (f *fakeTelegram) sentTo(chatID int64) []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentMessage
	for _, m := range f.sent {
		if m.ChatID == chatID {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeTelegram) setSendErrTo(errs map[int64]error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErrTo = errs
}

func (f *fakeTelegram) offsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.polled...)
}

func (f *fakeTelegram) setHold(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = d
}

func (f *fakeTelegram) SetReaction(_ context.Context, chatID, messageID int64, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, fakeReaction{ChatID: chatID, MessageID: messageID, Emoji: emoji})
	return f.reactionErr
}

func (f *fakeTelegram) sentReactions() []fakeReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReaction(nil), f.reactions...)
}

func (f *fakeTelegram) HoldRemaining() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hold
}

var _ telegramClient = (*fakeTelegram)(nil)

// structRoundTrip converts through structpb like the real Host: it rejects
// Go types such as []string, and numbers come back as float64.
func structRoundTrip(v map[string]any) (map[string]any, error) {
	st, err := structpb.NewStruct(v)
	if err != nil {
		return nil, err
	}
	return st.AsMap(), nil
}
