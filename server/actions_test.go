package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/fieldmapping"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

// redmineFixtureServer answers just enough of the Redmine JSON API for the
// integration tests below: auth validation, one issue by id (for
// link.set's resolve step), and a PUT handler that records the status_id it
// was sent (for write-back).
func redmineFixtureServer(t *testing.T, onPut func(statusID any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/current.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"id":1,"login":"alice"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/projects.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"projects":[],"total_count":0}`))
		case r.Method == http.MethodGet && r.URL.Path == "/issues/42.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"Fixture issue","status":{"id":2}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/issues/42.json":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if onPut != nil {
				issue, _ := body["issue"].(map[string]any)
				onPut(issue["status_id"])
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestPlugin() (*redminePlugin, *fakeHost) {
	p := &redminePlugin{}
	host := newFakeHost()
	p.SetHost(host)
	return p, host
}

func handle(t *testing.T, p *redminePlugin, key, workspaceID, taskID string, body any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := p.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: key,
		Context:   pluginsdk.VerifiedActionContext{WorkspaceID: workspaceID, TaskID: taskID},
		Body:      encoded,
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(resp.Body, &out))
	return out
}

func TestHandleAction_ConnectionSaveThenGet_RoundTrips(t *testing.T) {
	p, _ := newTestPlugin()
	srv := redmineFixtureServer(t, nil)

	saved := handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	require.Equal(t, "connected", saved["state"])

	got := handle(t, p, "connection.get", "ws-1", "", nil)
	require.Equal(t, "connected", got["state"])
}

func TestHandleAction_ConnectionSave_InvalidKey_ReturnsClassifiedError(t *testing.T) {
	p, _ := newTestPlugin()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	out := handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "bad-key"})
	require.Equal(t, "invalid_credentials", out["kind"])
}

func TestHandleAction_ProjectsSaveThenList_PersistsSelection(t *testing.T) {
	p, _ := newTestPlugin()
	srv := redmineFixtureServer(t, nil)
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})

	handle(t, p, "projects.save", "ws-1", "", map[string]any{"project_ids": []int{1, 2}})
	out := handle(t, p, "projects.list", "ws-1", "", nil)
	selected, ok := out["selected_ids"].([]any)
	require.True(t, ok)
	require.ElementsMatch(t, []any{float64(1), float64(2)}, selected)
}

func TestHandleAction_LinkSetThenGet_ResolvesAndPersistsLink(t *testing.T) {
	p, _ := newTestPlugin()
	srv := redmineFixtureServer(t, nil)
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})

	out := handle(t, p, "link.set", "ws-1", "task-1", map[string]any{"reference": "#42"})
	require.Equal(t, true, out["linked"])
	require.EqualValues(t, 42, out["issue_id"])

	got := handle(t, p, "link.get", "ws-1", "task-1", nil)
	require.Equal(t, true, got["linked"])
}

func TestHandleAction_LinkUnset_RemovesLink(t *testing.T) {
	p, _ := newTestPlugin()
	srv := redmineFixtureServer(t, nil)
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	handle(t, p, "link.set", "ws-1", "task-1", map[string]any{"reference": "#42"})

	handle(t, p, "link.unset", "ws-1", "task-1", nil)
	got := handle(t, p, "link.get", "ws-1", "task-1", nil)
	require.Equal(t, false, got["linked"])
}

func TestOnEvent_TaskMoved_PushesWritebackForLinkedTaskWithMappedStep(t *testing.T) {
	p, host := newTestPlugin()

	var pushedStatus any
	srv := redmineFixtureServer(t, func(statusID any) { pushedStatus = statusID })
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	handle(t, p, "link.set", "ws-1", "task-1", map[string]any{"reference": "#42"})

	require.NoError(t, p.fieldmappingSvc.Save(context.Background(), "ws-1", fieldmapping.Mapping{
		Statuses: []fieldmapping.StatusMapping{{RedmineStatusID: 5, WorkflowStepID: "step-done"}},
	}))
	// autoStatusWriteback left at its default (false, per GetOptions' zero value).

	err := p.OnEvent(context.Background(), &pluginsdk.Event{
		EventID: "e1", EventType: "task.moved",
		Payload: map[string]any{"task_id": "task-1", "to_step_id": "step-done"},
	})
	require.NoError(t, err)
	require.Nil(t, pushedStatus, "autoStatusWriteback defaults to false; OnEvent must not push")
	require.Empty(t, host.updateCalls())
}

func TestHandleAction_WorkflowsList_ReturnsWorkflowsWithSteps(t *testing.T) {
	p, _ := newTestPlugin()
	out := handle(t, p, "workflows.list", "ws-1", "", nil)
	workflows, ok := out["workflows"].([]any)
	require.True(t, ok)
	require.Len(t, workflows, 1)
	wf, ok := workflows[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "wf-1", wf["id"])
	steps, ok := wf["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 2)
}

func TestOnEvent_TaskMoved_AutoWritebackEnabled_PushesStatus(t *testing.T) {
	p, _ := newTestPlugin()

	var pushedStatus any
	srv := redmineFixtureServer(t, func(statusID any) { pushedStatus = statusID })
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	handle(t, p, "link.set", "ws-1", "task-1", map[string]any{"reference": "#42"})

	require.NoError(t, p.fieldmappingSvc.Save(context.Background(), "ws-1", fieldmapping.Mapping{
		Statuses: []fieldmapping.StatusMapping{{RedmineStatusID: 5, WorkflowStepID: "step-done"}},
	}))
	handle(t, p, "syncoptions.save", "ws-1", "", map[string]any{"auto_status_writeback": true})

	err := p.OnEvent(context.Background(), &pluginsdk.Event{
		EventID: "e1", EventType: "task.moved",
		Payload: map[string]any{"task_id": "task-1", "to_step_id": "step-done"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 5, pushedStatus)
}
