package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/fieldmapping"
	redminesync "kandev-plugin-redmine/internal/sync"
	"kandev-plugin-redmine/internal/watch"

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

func TestHandleAction_IssueUploadAndCreateExposeFullWriteContract(t *testing.T) {
	var uploadFilename, uploadContentType string
	var uploadBody []byte
	var createBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/uploads.json":
			uploadFilename = r.URL.Query().Get("filename")
			uploadContentType = r.Header.Get("Content-Type")
			uploadBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"upload":{"token":"upload-token"}}`))
		case "/issues.json":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"issue":{"id":77,"subject":"Created"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p, _ := newTestPlugin()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "key"})
	upload := handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "note.txt", "content_type": "text/plain", "content_base64": base64.StdEncoding.EncodeToString([]byte("hello"))})
	require.Equal(t, "upload-token", upload["token"])
	require.Equal(t, "note.txt", uploadFilename)
	require.Equal(t, "application/octet-stream", uploadContentType)
	require.Equal(t, "hello", string(uploadBody))

	// Feed the upload action response verbatim into create, as a UI caller
	// does. This proves snake_case content_type decodes into issues.Upload.
	created := handle(t, p, "issues.create", "ws-1", "", map[string]any{"project_id": 1, "tracker_id": 2, "status_id": 3, "priority_id": 4, "subject": "Created", "description": "Body", "custom_fields": []any{map[string]any{"id": 9, "name": "Tier", "value": "Gold"}}, "uploads": []any{upload}})
	require.EqualValues(t, 77, created["id"])
	issue, ok := createBody["issue"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, issue["project_id"])
	require.Equal(t, "Created", issue["subject"])
	uploads, ok := issue["uploads"].([]any)
	require.True(t, ok)
	require.Len(t, uploads, 1)
	createdUpload, ok := uploads[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upload-token", createdUpload["token"])
	require.Equal(t, "note.txt", createdUpload["filename"])
	require.Equal(t, "text/plain", createdUpload["content_type"])
}

func TestHandleAction_IssueUpdateValidatesIDAndSendsFullPayload(t *testing.T) {
	var updateBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/issues/23.json":
			require.Equal(t, http.MethodPut, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&updateBody))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p, _ := newTestPlugin()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "key"})

	invalid := handle(t, p, "issues.update", "ws-1", "", map[string]any{"subject": "missing ID"})
	require.Contains(t, invalid["error"], "issue_id")
	updated := handle(t, p, "issues.update", "ws-1", "", map[string]any{"issue_id": 23, "project_id": 1, "tracker_id": 2, "status_id": 3, "priority_id": 4, "subject": "Updated", "description": "Body", "custom_fields": []any{map[string]any{"id": 9, "name": "Tier", "value": "Gold"}}, "uploads": []any{map[string]any{"token": "u", "filename": "note.txt", "content_type": "text/plain"}}})
	require.Equal(t, true, updated["updated"])
	issue, ok := updateBody["issue"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Updated", issue["subject"])
	require.EqualValues(t, 3, issue["status_id"])
	uploads, ok := issue["uploads"].([]any)
	require.True(t, ok)
	require.Equal(t, "text/plain", uploads[0].(map[string]any)["content_type"])
}

func TestHandleAction_IssueUploadRejectsInvalidInput(t *testing.T) {
	p, _ := newTestPlugin()
	out := handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "", "content_base64": "aGVsbG8="})
	require.Contains(t, out["error"], "filename")
	out = handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "x", "content_base64": "not-base64"})
	require.Contains(t, out["error"], "invalid")
	out = handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "x", "content_base64": ""})
	require.Contains(t, out["error"], "required")
	ok := handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "x", "content_base64": base64.StdEncoding.EncodeToString(make([]byte, maxAttachmentBytes))})
	require.Contains(t, ok["error"], "no connection")
	out = handle(t, p, "issues.upload", "ws-1", "", map[string]any{"filename": "x", "content_base64": base64.StdEncoding.EncodeToString(make([]byte, maxAttachmentBytes+1))})
	require.Contains(t, out["error"], "exceeds")
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

func TestHandleAction_WatchesRejectForgedInvalidOrUnselectedInputs(t *testing.T) {
	p, _ := newTestPlugin()
	for _, body := range []map[string]any{
		{"project_id": 0, "max_inflight_tasks": 0},
		{"project_id": 1, "max_inflight_tasks": -1},
		{"project_id": 1, "max_inflight_tasks": 0, "tracker_id": -1},
		{"project_id": 1, "max_inflight_tasks": 0, "status_id": -1},
		{"project_id": 99, "max_inflight_tasks": 0},
	} {
		out := handle(t, p, "watches.create", "ws-1", "", body)
		require.NotEmpty(t, out["error"])
	}
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1}))
	out := handle(t, p, "watches.create", "ws-1", "", map[string]any{"project_id": 2, "max_inflight_tasks": 0})
	require.Contains(t, out["error"], "not selected")
}

func TestApplyWatchMapping_BackfillsLegacyWatchPlacementAndAttributes(t *testing.T) {
	statusID := 5
	legacy := watch.Watch{ID: "legacy", WorkspaceID: "ws-1", ProjectID: 1, StatusID: &statusID}
	require.True(t, needsWatchBackfill(legacy))
	backfilled := applyWatchMapping(legacy, fieldmapping.Mapping{
		WorkflowID: "wf-secondary",
		Statuses:   []fieldmapping.StatusMapping{{RedmineStatusID: 5, WorkflowStepID: "step-triage"}},
		Trackers:   []fieldmapping.TrackerMapping{{RedmineTrackerID: 3, TaskLabel: "bug"}},
		Priorities: []fieldmapping.PriorityMapping{{RedminePriorityID: 4, TaskPriority: "high"}},
	})
	require.Equal(t, "wf-secondary", backfilled.WorkflowID)
	require.Equal(t, "step-triage", backfilled.WorkflowStepID)
	require.Equal(t, "bug", backfilled.TrackerLabels[3])
	require.Equal(t, "high", backfilled.PriorityMappings[4])
	require.False(t, needsWatchBackfill(backfilled))
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

func TestOnEvent_WorkspaceDeleted_CleansPluginStateAfterTaskCascade(t *testing.T) {
	p, _ := newTestPlugin()
	srv := redmineFixtureServer(t, nil)
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1}))
	require.NoError(t, p.fieldmappingSvc.Save(context.Background(), "ws-1", fieldmapping.Mapping{}))
	require.NoError(t, p.syncSvc.SaveOptions(context.Background(), "ws-1", redminesync.Options{AutoStatusWriteback: true}))
	_, err := p.watchSvc.CreateWatch(context.Background(), watch.Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, p.tasklinkSvc.Set(context.Background(), "task-already-cascaded", "ws-1", 42, "url"))

	require.NoError(t, p.OnEvent(context.Background(), &pluginsdk.Event{EventID: "deleted", EventType: "workspace.deleted", WorkspaceID: "ws-1"}))
	_, found, err := p.connectionSvc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.False(t, found)
	watches, err := p.watchSvc.ListWatches(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Empty(t, watches)
	_, found, err = p.tasklinkSvc.Get(context.Background(), "task-already-cascaded")
	require.NoError(t, err)
	require.False(t, found)
}

func TestOnEvent_TaskDeletedIdempotentlyRemovesLink(t *testing.T) {
	p, _ := newTestPlugin()
	require.NoError(t, p.tasklinkSvc.Set(context.Background(), "task-1", "ws-1", 42, "url"))
	event := &pluginsdk.Event{EventID: "deleted", EventType: "task.deleted", Payload: map[string]any{"task_id": "task-1"}}
	require.NoError(t, p.OnEvent(context.Background(), event))
	require.NoError(t, p.OnEvent(context.Background(), event))
	_, found, err := p.tasklinkSvc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, found)
}
