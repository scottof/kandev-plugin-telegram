package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	topologyPageSize    = 100
	maxTriageWorkflows  = 12
	triageTimeout       = 3 * time.Minute
	maxTaskTitleRunes   = 200
	maxReasonRunes      = 300
	defaultColumnLabel  = "default column"
	defaultTriageAnswer = "Filed the task."
)

// topology is the workspace layout shown to the triage agent. Step ids stay
// out of the JSON: the agent names a column and the plugin maps it back.
type topology struct {
	Workspaces []topoWorkspace `json:"workspaces"`
}

type topoWorkspace struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Workflows    []topoWorkflow `json:"workflows"`
	Repositories []string       `json:"repositories,omitempty"`
}

type topoWorkflow struct {
	ID      string                   `json:"id"`
	Name    string                   `json:"name"`
	Columns []string                 `json:"columns"`
	steps   []pluginsdk.WorkflowStep `json:"-"`
}

// readTopology lists workspaces, their workflows (at most maxTriageWorkflows
// in total) with columns in board order, and repository names.
func readTopology(ctx context.Context, host pluginsdk.Host) (topology, error) {
	workspaces, _, err := host.Workspaces().List(ctx, pluginsdk.Page{Limit: topologyPageSize})
	if err != nil {
		return topology{}, fmt.Errorf("listing workspaces: %w", err)
	}
	if len(workspaces) == 0 {
		return topology{}, errors.New("no kandev workspaces exist yet")
	}

	var topo topology
	workflowCount := 0
	for _, ws := range workspaces {
		tw := topoWorkspace{ID: ws.ID, Name: ws.Name}
		workflows, _, err := host.Workflows().List(ctx, ws.ID, pluginsdk.Page{Limit: topologyPageSize})
		if err != nil {
			return topology{}, fmt.Errorf("listing workflows: %w", err)
		}
		for _, wf := range workflows {
			if workflowCount == maxTriageWorkflows {
				break
			}
			workflowCount++
			tf := topoWorkflow{ID: wf.ID, Name: wf.Name, Columns: []string{}}
			// A workflow whose steps cannot be read is still offered; the task
			// then lands in the host's default column.
			if steps, err := host.Workflows().ListSteps(ctx, wf.ID); err == nil {
				sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
				tf.steps = steps
				for _, s := range steps {
					tf.Columns = append(tf.Columns, s.Name)
				}
			}
			tw.Workflows = append(tw.Workflows, tf)
		}
		if repos, _, err := host.Repositories().List(ctx, ws.ID, pluginsdk.Page{Limit: topologyPageSize}); err == nil {
			for _, r := range repos {
				tw.Repositories = append(tw.Repositories, r.Name)
			}
		}
		topo.Workspaces = append(topo.Workspaces, tw)
	}
	return topo, nil
}

// triageRequest is one /new command.
type triageRequest struct {
	Sender       string
	Text         string
	ReplyContext string
}

const triageInstructions = `You triage requests sent to kandev from Telegram and decide where a new task belongs.
Reply with a single JSON object and nothing else, no prose and no code fence:
{"workspace_id": "", "workflow_id": "", "column": "", "title": "", "description": "", "reply": ""}

Rules:
- Copy workspace_id and workflow_id verbatim from the topology below. Never invent ids.
- column is one of that workflow's column names, or empty for the workflow's default column.
- If nothing clearly fits, use the first workspace and its first workflow.
- title is a short imperative summary of at most 80 characters.
- description holds everything an agent needs to do the work. The agent cannot see Telegram, so include all context from the request.
- reply is one short sentence for the requester. Do not ask follow-up questions.`

func buildTriagePrompt(topo topology, req triageRequest) (string, error) {
	data, err := json.MarshalIndent(topo, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding topology: %w", err)
	}
	var sb strings.Builder
	sb.WriteString(triageInstructions)
	sb.WriteString("\n\nTopology (JSON):\n")
	sb.Write(data)
	sb.WriteString("\n\nRequest from " + req.Sender + " via Telegram:\n" + req.Text)
	if req.ReplyContext != "" {
		sb.WriteString("\n\nEarlier message they replied to:\n" + req.ReplyContext)
	}
	return sb.String(), nil
}

type triageDecision struct {
	WorkspaceID string `json:"workspace_id"`
	WorkflowID  string `json:"workflow_id"`
	Column      string `json:"column"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Reply       string `json:"reply"`
}

// parseDecision accepts the JSON object even when the agent wraps it in a
// code fence or prose, by reading from the first '{' to the last '}'.
func parseDecision(resp string) (triageDecision, error) {
	resp = strings.TrimSpace(resp)
	if resp == "" {
		return triageDecision{}, errors.New("the triage agent returned an empty response")
	}
	start, end := strings.Index(resp, "{"), strings.LastIndex(resp, "}")
	if start < 0 || end < start {
		return triageDecision{}, errors.New("the triage agent did not return JSON")
	}
	var d triageDecision
	if err := json.Unmarshal([]byte(resp[start:end+1]), &d); err != nil {
		return triageDecision{}, fmt.Errorf("the triage agent did not return JSON: %w", err)
	}
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return triageDecision{}, errors.New("the triage agent returned no task title")
	}
	return d, nil
}

// placement is where the task is created, with names for the reply.
type placement struct {
	WorkspaceID   string
	WorkflowID    string
	StepID        *string
	workspaceName string
	workflowName  string
	columnName    string
}

func (p placement) label() string {
	column := p.columnName
	if column == "" {
		column = defaultColumnLabel
	}
	return p.workspaceName + " / " + p.workflowName + " / " + column
}

// resolve maps a decision onto real ids, falling back to the first workspace
// and workflow rather than rejecting an agent's slightly wrong answer.
func (t topology) resolve(d triageDecision) (placement, error) {
	ws := t.Workspaces[0]
	for _, w := range t.Workspaces {
		if w.ID == d.WorkspaceID {
			ws = w
			break
		}
	}
	if len(ws.Workflows) == 0 {
		return placement{}, fmt.Errorf("workspace %q has no workflows", ws.Name)
	}
	wf := ws.Workflows[0]
	for _, f := range ws.Workflows {
		if f.ID == d.WorkflowID {
			wf = f
			break
		}
	}
	p := placement{WorkspaceID: ws.ID, WorkflowID: wf.ID, workspaceName: ws.Name, workflowName: wf.Name}
	if column := strings.TrimSpace(d.Column); column != "" {
		for _, s := range wf.steps {
			if strings.EqualFold(s.Name, column) {
				id := s.ID
				p.StepID = &id
				p.columnName = s.Name
				break
			}
		}
	}
	return p, nil
}

// fileTask runs /new: triage, then create. Nothing is created when triage
// fails.
func (b *bot) fileTask(ctx context.Context, req triageRequest) outMessage {
	ctx, cancel := context.WithTimeout(ctx, triageTimeout)
	defer cancel()

	decision, place, err := b.triage(ctx, req)
	if err != nil {
		return outMessage{Text: "Triage failed: " + truncateRunes(err.Error(), maxReasonRunes) + ". No task was created."}
	}

	description := strings.TrimSpace(decision.Description)
	if description == "" {
		description = req.Text
	}
	task, err := b.host.Tasks().Create(ctx, pluginsdk.CreateTaskInput{
		WorkspaceID:    place.WorkspaceID,
		WorkflowID:     place.WorkflowID,
		WorkflowStepID: place.StepID,
		Title:          truncateRunes(decision.Title, maxTaskTitleRunes),
		Description:    description + "\n\n---\nFiled from Telegram by " + req.Sender + ".",
		StartAgent:     b.cfg.StartAgent,
	})
	if err != nil {
		return outMessage{Text: "Could not create the task: " + truncateRunes(err.Error(), maxReasonRunes)}
	}

	answer := strings.TrimSpace(decision.Reply)
	if answer == "" {
		answer = defaultTriageAnswer
	}
	heading := strings.TrimSpace(task.Identifier + " " + task.Title)
	text := truncateRunes(answer, maxDetailRunes) + "\n" + truncateRunes(heading, maxTitleRunes) + "\nPlaced in " + place.label()
	return outMessage{Text: capMessage(text), Buttons: []linkButton{{Text: "Open task", URL: taskURL(b.cfg.KandevURL, task.ID)}}}
}

func (b *bot) triage(ctx context.Context, req triageRequest) (triageDecision, placement, error) {
	topo, err := readTopology(ctx, b.host)
	if err != nil {
		return triageDecision{}, placement{}, err
	}
	prompt, err := buildTriagePrompt(topo, req)
	if err != nil {
		return triageDecision{}, placement{}, err
	}
	var opts []pluginsdk.UtilityAgentOptions
	if b.cfg.TriageAgentProfile != "" {
		opts = append(opts, pluginsdk.UtilityAgentOptions{ProfileID: b.cfg.TriageAgentProfile})
	}
	resp, err := b.host.InvokeUtilityAgent(ctx, prompt, opts...)
	if err != nil {
		return triageDecision{}, placement{}, err
	}
	decision, err := parseDecision(resp)
	if err != nil {
		return triageDecision{}, placement{}, err
	}
	place, err := topo.resolve(decision)
	return decision, place, err
}

func senderName(u *tgUser) string {
	switch {
	case u == nil:
		return "a Telegram user"
	case u.Username != "":
		return u.Username
	case u.FirstName != "":
		return u.FirstName
	default:
		return "a Telegram user"
	}
}

const newUsage = "Usage: /new <what needs doing>. Reply to a message to add it as context."
