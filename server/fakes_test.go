package main

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is an in-memory pluginsdk.Host. SetState round-trips values through
// JSON like the real Host's protobuf Struct, so numbers come back as float64.
type fakeHost struct {
	pluginsdk.UnimplementedHostData
	mu     sync.Mutex
	state  map[string]map[string]any
	config map[string]any
}

func newFakeHost() *fakeHost {
	return &fakeHost{state: make(map[string]map[string]any), config: map[string]any{}}
}

func fakeStateKey(scope, scopeID, key string) string {
	return scope + "/" + scopeID + "/" + key
}

func (h *fakeHost) GetState(_ context.Context, scope, scopeID, key string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.state[fakeStateKey(scope, scopeID, key)]
	return v, ok, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, scopeID, key string, value map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state[fakeStateKey(scope, scopeID, key)] = jsonRoundTrip(value)
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

func jsonRoundTrip(v map[string]any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}
