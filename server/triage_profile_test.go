package main

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const triageReplyWS1 = `{"workspace_id":"ws-1","workflow_id":"wf-1","title":"Write docs"}`

var errNoDefault = status.Error(codes.FailedPrecondition, "no default agent profile configured")

// newSingleWorkspaceTriageBot keeps only ws-1 and gives it defaultProfile as
// its default agent ("" leaves it unset).
func newSingleWorkspaceTriageBot(t *testing.T, defaultProfile string) (*bot, *fakeHost) {
	t.Helper()
	b, host, _ := newTriageBot(t)
	d := host.data()
	ws := d.workspaces[0]
	if defaultProfile != "" {
		ws.DefaultAgentProfileID = &defaultProfile
	}
	d.workspaces = []pluginsdk.Workspace{ws}
	d.agentReply = triageReplyWS1
	return b, host
}

func requireTriageFailure(t *testing.T, msg outMessage, want error) {
	t.Helper()
	require.Equal(t, "Triage failed: "+want.Error()+". No task was created.", msg.Text)
	require.NotContains(t, msg.Text, "rpc error")
	require.NotContains(t, msg.Text, "—")
	require.Empty(t, msg.Buttons)
}

func TestTriageProfile_UtilityDefaultIsTriedFirst(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "profile-ws")
	b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})
	d := host.data()
	require.Equal(t, []string{""}, d.agentProfileCalls, "a working utility default is used as is")
	require.Len(t, d.created, 1)
}

func TestTriageProfile_FallsBackToSingleWorkspaceDefault(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "profile-ws")
	host.data().agentErrByProfile = map[string]error{"": errNoDefault}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	d := host.data()
	require.Equal(t, []string{"", "profile-ws"}, d.agentProfileCalls)
	require.Len(t, d.created, 1)
	require.Contains(t, msg.Text, "Write docs")
}

func TestTriageProfile_NoFallbackWithSeveralWorkspaces(t *testing.T) {
	b, host, _ := newTriageBot(t)
	d := host.data()
	profile := "profile-ws"
	d.workspaces[0].DefaultAgentProfileID = &profile
	d.agentReply = triageReplyWS1
	d.agentErrByProfile = map[string]error{"": errNoDefault}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	require.Equal(t, []string{""}, d.agentProfileCalls, "with two workspaces there is no single default to use")
	require.Empty(t, d.created)
	requireTriageFailure(t, msg, errNoTriageProfile)
}

func TestTriageProfile_NoDefaultAnywhereNamesTheSettings(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "")
	host.data().agentErrByProfile = map[string]error{"": errNoDefault}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	require.Equal(t, []string{""}, host.data().agentProfileCalls)
	require.Empty(t, host.data().created)
	requireTriageFailure(t, msg, errNoTriageProfile)
	require.Contains(t, msg.Text, "Settings > Plugins > Telegram")
	require.Contains(t, msg.Text, "Settings > Utility Agents")
}

func TestTriageProfile_UnusableWorkspaceDefault(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "profile-cli")
	host.data().agentErrByProfile = map[string]error{
		"":            errNoDefault,
		"profile-cli": status.Error(codes.FailedPrecondition, "profile is a CLI passthrough"),
	}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	require.Equal(t, []string{"", "profile-cli"}, host.data().agentProfileCalls)
	require.Empty(t, host.data().created)
	requireTriageFailure(t, msg, errWorkspaceDefaultUnusable)
}

func TestTriageProfile_UnusablePluginProfileDoesNotFallBack(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "profile-ws")
	b.cfg.TriageAgentProfile = "profile-deleted"
	host.data().agentErrByProfile = map[string]error{
		"profile-deleted": status.Error(codes.FailedPrecondition, "profile not found"),
	}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	require.Equal(t, []string{"profile-deleted"}, host.data().agentProfileCalls, "an explicit choice is never replaced silently")
	require.Empty(t, host.data().created)
	requireTriageFailure(t, msg, errPluginProfileUnusable)
}

func TestTriageProfile_OtherErrorsDoNotFallBack(t *testing.T) {
	b, host := newSingleWorkspaceTriageBot(t, "profile-ws")
	host.data().agentErrByProfile = map[string]error{"": status.Error(codes.Unavailable, "agent runtime is starting")}

	msg := b.fileTask(context.Background(), triageRequest{Sender: "m", Text: "x"})

	require.Equal(t, []string{""}, host.data().agentProfileCalls)
	require.Empty(t, host.data().created)
	require.Contains(t, msg.Text, "agent runtime is starting")
}
