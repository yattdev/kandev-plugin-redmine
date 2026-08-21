package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func newIssuesService(t *testing.T, handler http.HandlerFunc) *issues.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := redmineclient.New(srv.URL, "key", srv.Client())
	return issues.New(client)
}

func oneIssuePage(id int, subject string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"issues":[{"id":%d,"subject":%q}],"total_count":1}`, id, subject)
	}
}

func TestCreateWatch_ThenPoll_CreatesOneTaskForMatchingIssue(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)

	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))

	require.Len(t, host.tasks, 1)
	for _, task := range host.tasks {
		require.Equal(t, watchObj.ID, task.Metadata[metadataKeyWatchID])
	}
}

func TestPoll_CreatesTaskInMappedWorkflowWithPriorityAndTrackerLabel(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{
		WorkspaceID: "ws-1", WorkflowID: "wf-secondary", WorkflowStepID: "step-triage", ProjectID: 1, Enabled: true,
		TrackerLabels: map[int]string{3: "bug"}, PriorityMappings: map[int]string{4: "high"},
	})
	require.NoError(t, err)
	issuesSvc := newIssuesService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"id":42,"subject":"New issue","tracker":{"id":3},"priority":{"id":4}}],"total_count":1}`))
	})
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))
	for _, task := range host.tasks {
		require.Equal(t, "high", task.Priority)
		require.Equal(t, []string{"bug"}, task.Labels)
	}
	require.Len(t, host.creates, 1)
	require.Equal(t, "wf-secondary", host.creates[0].WorkflowID)
	require.NotNil(t, host.creates[0].WorkflowStepID)
	require.Equal(t, "step-triage", *host.creates[0].WorkflowStepID)
}

func TestPoll_AlreadySeenIssue_CreatesNoSecondTask(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)

	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))

	require.Len(t, host.tasks, 1, "polling the same already-seen issue twice must not create a second task")
}

// TestThrottleCapEnforced is the task 07 acceptance #3 test: the throttle
// key the poller checks against must be *exactly* the same constant the
// plugin writes into a created task's metadata (metadataKeyWatchID) —
// otherwise the cap silently never applies (the precise bug Sentry's native
// integration originally shipped with).
func TestThrottleCapEnforced(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true, MaxInflightTasks: 1})
	require.NoError(t, err)

	// First matching issue: creates a task (0 inflight -> under the cap).
	issuesSvc1 := newIssuesService(t, oneIssuePage(42, "First"))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc1))
	require.Len(t, host.tasks, 1)

	// Second matching issue while the first task is still open (RUNNING,
	// non-terminal): the cap must refuse a second task.
	issuesSvc2 := newIssuesService(t, oneIssuePage(99, "Second"))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc2))
	require.Len(t, host.tasks, 1, "maxInflightTasks:1 with one open task must refuse a second task")

	// Close the first task; the throttle must now admit the second.
	for id := range host.tasks {
		host.setTaskState(id, "COMPLETED")
	}
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc2))
	require.Len(t, host.tasks, 2, "once the first task closes, a newly matching issue must be admitted")
}

func TestThrottleCapEnforced_UnlimitedWhenZero(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true, MaxInflightTasks: 0})
	require.NoError(t, err)

	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(1, "a"))))
	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(2, "b"))))
	require.Len(t, host.tasks, 2)
}

func TestPoll_DisabledWatch_IsNoOp(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: false})
	require.NoError(t, err)

	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(42, "x"))))
	require.Empty(t, host.tasks)
}

func TestDeleteWatch_CascadesTaskTreeDeleteAndRemovesFromList(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(42, "x"))))
	require.Len(t, host.tasks, 1)

	require.NoError(t, svc.DeleteWatch(context.Background(), "ws-1", watchObj.ID))

	require.Empty(t, host.tasks, "deleting the watch must cascade-delete its created tasks via PluginOwnedTaskTrees")
	watches, err := svc.ListWatches(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Empty(t, watches)
}

func TestListWatches_MultipleWatches_IndependentOfEachOther(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	w1, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	_, err = svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 2, Enabled: true})
	require.NoError(t, err)

	require.NoError(t, svc.DeleteWatch(context.Background(), "ws-1", w1.ID))

	watches, err := svc.ListWatches(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Len(t, watches, 1)
}

func TestFilter_TrackerAndStatusRestrictMatches(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	trackerID := 3
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true, TrackerID: &trackerID})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issues":[
			{"id":1,"subject":"wrong tracker","tracker":{"id":9}},
			{"id":2,"subject":"right tracker","tracker":{"id":3}}
		],"total_count":2}`))
	}))
	defer srv.Close()
	client := redmineclient.New(srv.URL, "key", srv.Client())
	issuesSvc := issues.New(client)

	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))
	require.Len(t, host.tasks, 1)
	for _, task := range host.tasks {
		require.Contains(t, task.Title, "right tracker")
	}
}

func TestPoll_PaginatesBeyondOneHundredIssues(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)

	issuesSvc := newIssuesService(t, func(res http.ResponseWriter, req *http.Request) {
		offset := req.URL.Query().Get("offset")
		start, end := 0, 100
		if offset == "100" {
			start, end = 100, 101
		}
		_, _ = fmt.Fprint(res, `{"issues":[`)
		for i := start; i < end; i++ {
			if i > start {
				_, _ = fmt.Fprint(res, ",")
			}
			_, _ = fmt.Fprintf(res, `{"id":%d,"subject":"issue %d"}`, i+1, i+1)
		}
		_, _ = fmt.Fprint(res, `],"total_count":101}`)
	})
	require.NoError(t, svc.Poll(context.Background(), w, issuesSvc))
	require.Len(t, host.tasks, 101)
}
