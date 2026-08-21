package sync

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// fakeHost is an in-memory pluginsdk.Host test double with a capturing
// TaskReader.Update, so tests can assert exactly what internal/sync wrote
// back to a task (workflow step / title / description) per call.
type fakeHost struct {
	pluginsdk.UnimplementedHostData

	mu        sync.Mutex
	state     map[string]map[string]any
	updates   []pluginsdk.UpdateTaskInput
	updateErr error
	task      pluginsdk.Task
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

func (h *fakeHost) Tasks() pluginsdk.TaskReader {
	return fakeTaskReader{TaskReader: pluginsdk.UnimplementedHostData{}.Tasks(), host: h}
}

func (h *fakeHost) updateCalls() []pluginsdk.UpdateTaskInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pluginsdk.UpdateTaskInput(nil), h.updates...)
}

// fakeTaskReader embeds the SDK's own unimplemented TaskReader (via
// UnimplementedHostData{}.Tasks()) so List/Get/Create fall through to its
// defaults; only Update is overridden, since that's all internal/sync calls.
type fakeTaskReader struct {
	pluginsdk.TaskReader
	host *fakeHost
}

func (r fakeTaskReader) Update(_ context.Context, in pluginsdk.UpdateTaskInput) (*pluginsdk.Task, error) {
	r.host.mu.Lock()
	r.host.updates = append(r.host.updates, in)
	err := r.host.updateErr
	r.host.mu.Unlock()
	if err != nil {
		return nil, err
	}
	r.host.mu.Lock()
	if in.Title != nil {
		r.host.task.Title = *in.Title
	}
	if in.Description != nil {
		r.host.task.Description = *in.Description
	}
	if in.WorkflowStepID != nil {
		r.host.task.WorkflowStepID = *in.WorkflowStepID
	}
	r.host.mu.Unlock()
	return &pluginsdk.Task{ID: in.ID}, nil
}

func (r fakeTaskReader) Get(_ context.Context, id string) (*pluginsdk.Task, error) {
	r.host.mu.Lock()
	defer r.host.mu.Unlock()
	task := r.host.task
	task.ID = id
	return &task, nil
}

var _ pluginsdk.Host = (*fakeHost)(nil)
