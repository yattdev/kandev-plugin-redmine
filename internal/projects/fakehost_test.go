package projects

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is an in-memory pluginsdk.Host test double (state only — this
// package never touches secrets). See internal/connection/fakehost_test.go
// for the JSON-round-trip rationale.
type fakeHost struct {
	pluginsdk.UnimplementedHostData
	mu    sync.Mutex
	state map[string]map[string]any
}

func newFakeHost() *fakeHost {
	return &fakeHost{state: make(map[string]map[string]any)}
}

func key(scope, scopeID, k string) string { return scope + "/" + scopeID + "/" + k }

func (h *fakeHost) GetState(_ context.Context, scope, scopeID, k string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.state[key(scope, scopeID, k)]
	return v, ok, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, scopeID, k string, value map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	h.state[key(scope, scopeID, k)] = out
	return nil
}

func (h *fakeHost) DeleteState(_ context.Context, scope, scopeID, k string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, key(scope, scopeID, k))
	return nil
}

func (h *fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}
func (h *fakeHost) GetConfig(context.Context) (map[string]any, error) { return map[string]any{}, nil }
func (h *fakeHost) RevealSecret(context.Context, string) (string, error) {
	return "", nil
}
func (h *fakeHost) GetSecret(context.Context, string) (string, bool, error) { return "", false, nil }
func (h *fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (h *fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (h *fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

var _ pluginsdk.Host = (*fakeHost)(nil)
