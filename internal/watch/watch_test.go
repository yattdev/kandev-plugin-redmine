package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newIssuesService(t *testing.T, handler http.HandlerFunc) *issues.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := redmineclient.New(srv.URL, "key", srv.Client())
	return issues.New(client)
}

func TestPoll_ConcurrentCallsCreateOnlyOneTask(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.Poll(context.Background(), w, issuesSvc) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, host.tasks, 1)
}

func TestPoll_RacingDeleteLeavesNoOwnedTaskOrIndex(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- svc.Poll(context.Background(), w, issuesSvc) }()
	go func() { defer wg.Done(); errs <- svc.DeleteWatch(context.Background(), "ws-1", w.ID) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Empty(t, host.tasks)
	watches, err := svc.ListWatches(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Empty(t, watches)
	tasks, err := svc.watchTasks(context.Background(), "ws-1", w.ID)
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestPoll_RecordFailureCompensatesCreatedTaskAndRetryCreatesOnce(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	host.setStateErr = fmt.Errorf("state unavailable")
	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	require.Error(t, svc.Poll(context.Background(), w, issuesSvc))
	require.Empty(t, host.tasks)
	require.Len(t, host.deletedTaskIDs(), 1)
	_, found, err := svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, svc.Poll(context.Background(), w, issuesSvc))
	require.Len(t, host.tasks, 1)
}

func TestPoll_WatchIndexFailureCompensatesTaskAndLink(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	// Link persistence performs two writes, the tracker marker is the third,
	// and the watcher dedup index is the fourth.
	host.setStateCalls = 0
	host.failSetStateAt = 4
	require.Error(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "x"))))
	require.Empty(t, host.tasks)
	tasks, err := svc.watchTasks(context.Background(), "ws-1", w.ID)
	require.NoError(t, err)
	require.Empty(t, tasks)
	_, found, err := svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPoll_TrackerMarkerFailureCompensatesTaskAndLink(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{
		WorkspaceID: "ws-1", ProjectID: 1, Enabled: true,
		TrackerLabels: map[int]string{3: "bug"},
	})
	require.NoError(t, err)
	host.setStateCalls = 0
	host.failSetStateAt = 3
	issuesSvc := newIssuesService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"id":42,"subject":"x","tracker":{"id":3}}],"total_count":1}`))
	})

	require.ErrorContains(t, svc.Poll(context.Background(), w, issuesSvc), "recording tracker label")
	require.Empty(t, host.tasks)
	require.Len(t, host.deletedTaskIDs(), 1)
	_, found, err := svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPoll_ThrottlePropagatesTransientTaskReadFailure(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true, MaxInflightTasks: 1})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "first"))))
	host.getTaskErr = fmt.Errorf("temporary host failure")
	require.ErrorContains(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(99, "second"))), "reading task")
	host.getTaskErr = status.Error(codes.NotFound, "gone")
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(99, "second"))))
}

func TestPoll_RacingClearWorkspaceLeavesNoOwnedTaskOrWatch(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- svc.Poll(context.Background(), w, issuesSvc) }()
	go func() { defer wg.Done(); errs <- svc.ClearWorkspace(context.Background(), "ws-1") }()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Empty(t, host.tasks)
	watches, err := svc.ListWatches(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Empty(t, watches)
}

func oneIssuePage(id int, subject string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"issues":[{"id":%d,"subject":%q}],"total_count":1}`, id, subject)
	}
}

func TestCreateWatch_ThenPoll_CreatesOneTaskForMatchingIssue(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)

	issuesSvc := newIssuesService(t, oneIssuePage(42, "New issue"))
	require.NoError(t, svc.Poll(context.Background(), watchObj, issuesSvc))

	require.Len(t, host.tasks, 1)
	for _, task := range host.tasks {
		require.Equal(t, watchObj.ID, task.Metadata[metadataKeyWatchID])
	}
}

func TestPoll_CreatedTaskIsLinkedAndReverseIndexed(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "linked"))))

	taskID := svc.mustWatchTaskID(t, "ws-1", w.ID, 42)
	link, found, err := svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 42, link.IssueID)
	require.Equal(t, "ws-1", link.WorkspaceID)
	resolved, found, err := svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, taskID, resolved)

	// Both the watcher dedup index and shared reverse index suppress a second
	// task for the same issue.
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "linked"))))
	require.Len(t, host.tasks, 1)

	require.NoError(t, svc.DeleteWatch(context.Background(), "ws-1", w.ID))
	_, found, err = svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPoll_CreatedTaskLinkRecordsOwnedTrackerLabel(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{
		WorkspaceID: "ws-1", ProjectID: 1, Enabled: true,
		TrackerLabels: map[int]string{3: "bug"},
	})
	require.NoError(t, err)
	issuesSvc := newIssuesService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"id":42,"subject":"linked","tracker":{"id":3}}],"total_count":1}`))
	})
	require.NoError(t, svc.Poll(context.Background(), w, issuesSvc))

	taskID := svc.mustWatchTaskID(t, "ws-1", w.ID, 42)
	link, found, err := svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "bug", link.AppliedTrackerLabel)
}

func TestDeleteWatchFailsClosedWithoutTaskTreeManager(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "linked"))))
	taskID := svc.mustWatchTaskID(t, "ws-1", w.ID, 42)

	failClosed := New(hostWithoutTaskTrees{Host: host}, svc.tasklinks)
	require.ErrorContains(t, failClosed.DeleteWatch(context.Background(), "ws-1", w.ID), "lacks PluginOwnedTaskTrees")
	require.Contains(t, host.tasks, taskID)
	_, found, err := svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.True(t, found)
}

func TestDeleteWatchRejectsDifferentWorkspaceLinkBeforeTaskDelete(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "linked"))))
	taskID := svc.mustWatchTaskID(t, "ws-1", w.ID, 42)
	require.NoError(t, svc.tasklinks.Set(context.Background(), taskID, "ws-2", 42, "https://other.example/issues/42"))

	require.ErrorContains(t, svc.DeleteWatch(context.Background(), "ws-1", w.ID), "link owned by workspace ws-2")
	require.Contains(t, host.tasks, taskID)
	link, found, err := svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "ws-2", link.WorkspaceID)
}

func TestClearWorkspaceRemovesWatcherTaskLinks(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "linked"))))
	taskID := svc.mustWatchTaskID(t, "ws-1", w.ID, 42)

	require.NoError(t, svc.ClearWorkspace(context.Background(), "ws-1"))
	require.NotContains(t, host.tasks, taskID)
	_, found, err := svc.tasklinks.Get(context.Background(), taskID)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = svc.tasklinks.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func (s *Service) mustWatchTaskID(t *testing.T, workspaceID, watchID string, issueID int) string {
	t.Helper()
	tasks, err := s.watchTasks(context.Background(), workspaceID, watchID)
	require.NoError(t, err)
	return tasks[issueID]
}

func TestPoll_CreatesTaskInMappedWorkflowWithPriorityAndTrackerLabel(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
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
	svc := newWatchService(host)
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
	svc := newWatchService(host)
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
	svc := newWatchService(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true, MaxInflightTasks: 0})
	require.NoError(t, err)

	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(1, "a"))))
	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(2, "b"))))
	require.Len(t, host.tasks, 2)
}

func TestPoll_DisabledWatch_IsNoOp(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	watchObj, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: false})
	require.NoError(t, err)

	require.NoError(t, svc.Poll(context.Background(), watchObj, newIssuesService(t, oneIssuePage(42, "x"))))
	require.Empty(t, host.tasks)
}

func TestPoll_PropagatesWatchListReadFailure(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
	w, err := svc.CreateWatch(context.Background(), Watch{WorkspaceID: "ws-1", ProjectID: 1, Enabled: true})
	require.NoError(t, err)
	host.getStateErr = fmt.Errorf("state unavailable")
	err = svc.Poll(context.Background(), w, newIssuesService(t, oneIssuePage(42, "x")))
	require.ErrorContains(t, err, "reading watches")
}

func TestDeleteWatch_CascadesTaskTreeDeleteAndRemovesFromList(t *testing.T) {
	host := newFakeHost()
	svc := newWatchService(host)
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
	svc := newWatchService(host)
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
	svc := newWatchService(host)
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
	svc := newWatchService(host)
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
