package connection

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is an in-memory pluginsdk.Host test double, shared by every test
// file in this package. It actually stores state/secrets (not just records
// calls), and round-trips SetState values through JSON exactly like the real
// Host's protobuf Struct conversion — so a test reading back a []string that
// was actually stored as []any (numbers as float64, etc.) catches the same
// class of bug the real wire format would.
type fakeHost struct {
	pluginsdk.UnimplementedHostData

	mu           sync.Mutex
	state        map[string]map[string]any
	secrets      map[string]string
	setSecretErr error
	setStateErr  map[string]error
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		state:       make(map[string]map[string]any),
		secrets:     make(map[string]string),
		setStateErr: make(map[string]error),
	}
}

func stateKeyOf(scope, scopeID, key string) string { return scope + "/" + scopeID + "/" + key }

func (h *fakeHost) GetState(_ context.Context, scope, scopeID, key string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.state[stateKeyOf(scope, scopeID, key)]
	return v, ok, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, scopeID, key string, value map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	stateKey := stateKeyOf(scope, scopeID, key)
	if err := h.setStateErr[stateKey]; err != nil {
		delete(h.setStateErr, stateKey)
		return err
	}
	h.state[stateKey] = jsonRoundTrip(value)
	return nil
}

func (h *fakeHost) failNextStateWrite(scope, scopeID, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setStateErr[stateKeyOf(scope, scopeID, key)] = errors.New("temporary state-store failure")
}

func (h *fakeHost) DeleteState(_ context.Context, scope, scopeID, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, stateKeyOf(scope, scopeID, key))
	return nil
}

func (h *fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}

func (h *fakeHost) GetConfig(context.Context) (map[string]any, error) { return map[string]any{}, nil }
func (h *fakeHost) RevealSecret(context.Context, string) (string, error) {
	return "", nil
}

func (h *fakeHost) GetSecret(_ context.Context, key string) (string, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.secrets[key]
	return v, ok, nil
}

func (h *fakeHost) SetSecret(_ context.Context, key, value string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.setSecretErr != nil {
		return h.setSecretErr
	}
	h.secrets[key] = value
	return nil
}

func (h *fakeHost) failSecretWrites() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setSecretErr = errors.New("temporary secret-store failure")
}

func (h *fakeHost) DeleteSecret(_ context.Context, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.secrets, key)
	return nil
}

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
