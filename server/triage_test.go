package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func seedTopology(host *fakeHost) {
	d := host.data()
	d.workspaces = []pluginsdk.Workspace{{ID: "ws-1", Name: "Personal"}, {ID: "ws-2", Name: "Client"}}
	d.workflows = []pluginsdk.Workflow{
		{ID: "wf-1", WorkspaceID: "ws-1", Name: "Dev"},
		{ID: "wf-2", WorkspaceID: "ws-2", Name: "Support"},
	}
	d.steps = map[string][]pluginsdk.WorkflowStep{
		"wf-1": {{ID: "step-doing", Name: "Doing", Position: 2}, {ID: "step-backlog", Name: "Backlog", Position: 1}},
		"wf-2": {{ID: "step-inbox", Name: "Inbox", Position: 1}},
	}
	d.repositories = []pluginsdk.Repository{{ID: "repo-1", WorkspaceID: "ws-1", Name: "kandev-telegram"}}
}

func TestReadTopology(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)
	require.Len(t, topo.Workspaces, 2)
	ws := topo.Workspaces[0]
	require.Equal(t, "ws-1", ws.ID)
	require.Equal(t, []string{"kandev-telegram"}, ws.Repositories)
	require.Equal(t, []string{"Backlog", "Doing"}, ws.Workflows[0].Columns, "columns follow step position")
}

func TestReadTopology_NoWorkspaces(t *testing.T) {
	_, err := readTopology(context.Background(), newFakeHost())
	require.EqualError(t, err, "no kandev workspaces exist yet")
}

func TestTopologyJSONHidesStepIDs(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)
	data, err := json.Marshal(topo)
	require.NoError(t, err)
	require.NotContains(t, string(data), "step-backlog")
	require.Contains(t, string(data), "Backlog")
}

func TestBuildTriagePrompt(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)

	prompt, err := buildTriagePrompt(topo, triageRequest{Sender: "michele", Text: "Fix the flaky login test", ReplyContext: "Needs permission: Fix login"})
	require.NoError(t, err)
	require.Contains(t, prompt, "single JSON object")
	require.Contains(t, prompt, `"id": "ws-1"`)
	require.Contains(t, prompt, "Request from michele via Telegram:\nFix the flaky login test")
	require.Contains(t, prompt, "Earlier message they replied to:\nNeeds permission: Fix login")
	require.NotContains(t, prompt, "—")

	prompt, err = buildTriagePrompt(topo, triageRequest{Sender: "michele", Text: "x"})
	require.NoError(t, err)
	require.NotContains(t, prompt, "Earlier message")
}

func TestParseDecision(t *testing.T) {
	want := triageDecision{WorkspaceID: "ws-1", WorkflowID: "wf-1", Column: "Backlog", Title: "Fix login", Description: "d", Reply: "Filed."}
	raw := `{"workspace_id":"ws-1","workflow_id":"wf-1","column":"Backlog","title":"Fix login","description":"d","reply":"Filed."}`
	for _, resp := range []string{
		raw,
		"```json\n" + raw + "\n```",
		"Sure, here it is:\n" + raw + "\nHope that helps.",
	} {
		got, err := parseDecision(resp)
		require.NoError(t, err, resp)
		require.Equal(t, want, got)
	}
}

func TestParseDecision_Errors(t *testing.T) {
	_, err := parseDecision("   ")
	require.EqualError(t, err, "the triage agent returned an empty response")
	_, err = parseDecision("I cannot help with that")
	require.ErrorContains(t, err, "did not return JSON")
	_, err = parseDecision(`{"workspace_id":"ws-1","title":"  "}`)
	require.EqualError(t, err, "the triage agent returned no task title")
}

func TestResolve(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)

	p, err := topo.resolve(triageDecision{WorkspaceID: "ws-2", WorkflowID: "wf-2", Column: "inbox"})
	require.NoError(t, err)
	require.Equal(t, "ws-2", p.WorkspaceID)
	require.Equal(t, "wf-2", p.WorkflowID)
	require.Equal(t, "step-inbox", *p.StepID)
	require.Equal(t, "Client / Support / Inbox", p.label())

	p, err = topo.resolve(triageDecision{WorkspaceID: "nope", WorkflowID: "nope", Column: "Nowhere"})
	require.NoError(t, err)
	require.Equal(t, "ws-1", p.WorkspaceID, "unknown workspace falls back to the first")
	require.Equal(t, "wf-1", p.WorkflowID)
	require.Nil(t, p.StepID, "unknown column leaves the host default")
	require.Equal(t, "Personal / Dev / Backlog", p.label(), "the reply names the start column the host files into")

	p, err = topo.resolve(triageDecision{WorkspaceID: "ws-1", WorkflowID: "wf-2"})
	require.NoError(t, err)
	require.Equal(t, "wf-1", p.WorkflowID, "a workflow from another workspace is not used")
}

func TestResolve_WorkspaceWithoutWorkflows(t *testing.T) {
	topo := topology{Workspaces: []topoWorkspace{{ID: "ws-1", Name: "Empty"}}}
	_, err := topo.resolve(triageDecision{WorkspaceID: "ws-1"})
	require.EqualError(t, err, `workspace "Empty" has no workflows`)
}

func newTriageBot(t *testing.T) (*bot, *fakeHost, *fakeTelegram) {
	t.Helper()
	host := newFakeHost()
	seedTopology(host)
	tg := newFakeTelegram()
	b := newTestBot(t, host, tg)
	return b, host, tg
}

func TestFileTask_CreatesTaskAndReplies(t *testing.T) {
	b, host, _ := newTriageBot(t)
	b.cfg.TriageAgentProfile = "profile-1"
	b.cfg.StartAgent = true
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","column":"Backlog","title":"Fix flaky login test","description":"The login test fails 1 in 5 runs.","reply":"Filed it in Backlog."}`

	msg := b.fileTask(context.Background(), triageRequest{Sender: "michele", Text: "fix the flaky login test"})

	d := host.data()
	require.Equal(t, []pluginsdk.UtilityAgentOptions{{ProfileID: "profile-1"}}, d.agentOptions)
	require.Len(t, d.created, 1)
	in := d.created[0]
	require.Equal(t, "ws-1", in.WorkspaceID)
	require.Equal(t, "wf-1", in.WorkflowID)
	require.Equal(t, "step-backlog", *in.WorkflowStepID)
	require.Equal(t, "Fix flaky login test", in.Title)
	require.True(t, in.StartAgent)
	require.Equal(t, "The login test fails 1 in 5 runs.\n\n---\nFiled from Telegram by michele.", in.Description)

	require.Equal(t, "Filed it in Backlog.\nKAN-7 Fix flaky login test\nPlaced in Personal / Dev / Backlog", msg.Text)
	require.Equal(t, []linkButton{{Text: "Open task", URL: testBase + "/t/task-new"}}, msg.Buttons)
}

func TestFileTask_DefaultProfileWhenUnset(t *testing.T) {
	b, host, _ := newTriageBot(t)
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"T"}`
	b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})
	require.Empty(t, host.data().agentOptions, "no options means the platform default profile")
}

func TestFileTask_AgentErrorCreatesNothing(t *testing.T) {
	b, host, _ := newTriageBot(t)
	host.data().agentErr = errors.New("rpc error: code = FailedPrecondition desc = profile not eligible")
	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})
	require.Empty(t, host.data().created)
	require.True(t, strings.HasPrefix(msg.Text, "Triage failed: "), msg.Text)
	require.Contains(t, msg.Text, "profile not eligible")
	require.True(t, strings.HasSuffix(msg.Text, "No task was created."))
	require.Empty(t, msg.Buttons)
}

func TestFileTask_BadAgentReplyCreatesNothing(t *testing.T) {
	b, host, _ := newTriageBot(t)
	host.data().agentReply = "I think it belongs in Backlog"
	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})
	require.Empty(t, host.data().created)
	require.Contains(t, msg.Text, "Triage failed: the triage agent did not return JSON")
}

func TestFileTask_CreateErrorIsReported(t *testing.T) {
	b, host, _ := newTriageBot(t)
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"T"}`
	host.data().createErr = errors.New("permission denied: api_write:tasks")
	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})
	require.Equal(t, "Could not create the task: permission denied: api_write:tasks", msg.Text)
}

func TestPollOnce_NewCommand(t *testing.T) {
	b, host, tg := newTriageBot(t)
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"Write docs"}`
	update := privateText(1, 42, "/new write the docs")
	update.Message.From.Username = "michele"
	update.Message.ReplyToMessage = &tgMessage{MessageID: 3, Text: "Finished: Fix login"}
	tg.queueUpdates(update, privateText(2, 42, "/new"))

	_, err := b.pollOnce(context.Background(), 0)
	require.NoError(t, err)
	d := host.data()
	require.Len(t, d.agentPrompts, 1)
	require.Contains(t, d.agentPrompts[0], "Request from michele via Telegram:\nwrite the docs")
	require.Contains(t, d.agentPrompts[0], "Earlier message they replied to:\nFinished: Fix login")

	sent := tg.sentMessages()
	require.Len(t, sent, 2)
	require.Contains(t, sent[0].Msg.Text, "KAN-7 Write docs")
	require.Equal(t, "Usage: /new <what needs doing>. Reply to a message to add it as context.", sent[1].Msg.Text)
}
