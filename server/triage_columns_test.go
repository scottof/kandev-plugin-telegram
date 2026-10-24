package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

// seedDoneColumn gives wf-1 a start column and a Done column.
func seedDoneColumn(host *fakeHost) {
	host.data().steps["wf-1"] = []pluginsdk.WorkflowStep{
		{ID: "step-backlog", Name: "Backlog", Position: 1},
		{ID: "step-todo", Name: "To do", Position: 2, IsStartStep: true},
		{ID: "step-done", Name: "Done", Position: 3},
	}
}

func TestReadTopology_NamesTheStartColumn(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	seedDoneColumn(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)
	require.Equal(t, "To do", topo.Workspaces[0].Workflows[0].StartColumn, "kandev's start step wins over the first column")
	require.Equal(t, "Inbox", topo.Workspaces[1].Workflows[0].StartColumn, "without a start step, the first column")

	prompt, err := buildTriagePrompt(topo, triageRequest{Sender: "m", Text: "x"})
	require.NoError(t, err)
	require.Contains(t, prompt, `"start_column": "To do"`)
	require.Contains(t, prompt, "Never pick a column for finished work")
}

func TestResolve_FinishingColumnGoesToTheStartColumn(t *testing.T) {
	host := newFakeHost()
	seedTopology(host)
	seedDoneColumn(host)
	topo, err := readTopology(context.Background(), host)
	require.NoError(t, err)

	for _, column := range []string{"Done", " done ", "COMPLETED", "Shipped"} {
		p, err := topo.resolve(triageDecision{WorkspaceID: "ws-1", WorkflowID: "wf-1", Column: column})
		require.NoError(t, err)
		require.Nil(t, p.StepID, column)
		require.Equal(t, "Personal / Dev / To do", p.label(), column)
	}
}

func TestFileTask_TaskCreatedCompletedIsMovedBack(t *testing.T) {
	b, host, _ := newTriageBot(t)
	seedDoneColumn(host)
	// "Wrapped up" completes tasks but is not a known finishing name.
	host.data().steps["wf-1"] = append(host.data().steps["wf-1"], pluginsdk.WorkflowStep{ID: "step-wrap", Name: "Wrapped up", Position: 4})
	d := host.data()
	d.agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","column":"Wrapped up","title":"Write docs"}`
	d.createdState = "COMPLETED"

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "write docs"})

	require.Equal(t, []pluginsdk.MoveTaskInput{{TaskID: "task-new", WorkflowStepID: "step-todo"}}, d.moves)
	require.Contains(t, msg.Text, "Placed in Personal / Dev / To do")
	require.Contains(t, msg.Text, "Moved from Wrapped up to To do, because Wrapped up completes tasks.")
}

func TestFileTask_FailedMoveBackIsReported(t *testing.T) {
	b, host, _ := newTriageBot(t)
	seedDoneColumn(host)
	host.data().steps["wf-1"] = append(host.data().steps["wf-1"], pluginsdk.WorkflowStep{ID: "step-wrap", Name: "Wrapped up", Position: 4})
	d := host.data()
	d.agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","column":"Wrapped up","title":"Write docs"}`
	d.createdState = "COMPLETED"
	d.moveErr = errors.New("permission denied")

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "write docs"})
	require.Contains(t, msg.Text, "Wrapped up completes tasks, so the task is already completed. Moving it back failed: permission denied")
}

func TestFileTask_OpenTaskIsNotMoved(t *testing.T) {
	b, host, _ := newTriageBot(t)
	seedDoneColumn(host)
	host.data().agentReply = `{"workspace_id":"ws-1","workflow_id":"wf-1","column":"Backlog","title":"Write docs"}`

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "write docs"})
	require.Empty(t, host.data().moves)
	require.Contains(t, msg.Text, "Placed in Personal / Dev / Backlog")
	require.NotContains(t, msg.Text, "Moved from")
}
