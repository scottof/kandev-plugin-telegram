package main

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestOnEvent_NoHost_ReturnsNil(t *testing.T) {
	p := &telegramPlugin{}
	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventID: "e1", EventType: "task.state_changed"})
	require.NoError(t, err)
}
