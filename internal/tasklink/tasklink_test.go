package tasklink

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetAndGet_RoundTrips(t *testing.T) {
	svc := New(newFakeHost())

	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "https://redmine.example/issues/42"))

	link, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 42, link.IssueID)
	require.Equal(t, "https://redmine.example/issues/42", link.IssueURL)
	require.Equal(t, "ws-1", link.WorkspaceID)
}

func TestConcurrentEchoMarkerWritesPreserveStatusAndTitleDescription(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- svc.RecordPushedStatus(context.Background(), "task-1", 5) }()
	go func() {
		defer wg.Done()
		errs <- svc.RecordPushedTitleAndDescription(context.Background(), "task-1", "Title", "Description")
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	link, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, link.LastPushedStatusID)
	require.Equal(t, 5, *link.LastPushedStatusID)
	require.Equal(t, "Title", link.LastPushedTitle)
	require.Equal(t, HashDescription("Description"), link.LastPushedDescriptionHash)
}

func TestGet_NotLinked_ReturnsNotFound(t *testing.T) {
	svc := New(newFakeHost())
	_, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, found)
}

func TestTaskIDForIssue_ResolvesReverseIndex(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "https://redmine.example/issues/42"))

	taskID, found, err := svc.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "task-1", taskID)

	_, found, err = svc.TaskIDForIssue(context.Background(), "ws-1", 999)
	require.NoError(t, err)
	require.False(t, found)
}

func TestTaskIDForIssue_DoesNotLeakAcrossWorkspaces(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))

	_, found, err := svc.TaskIDForIssue(context.Background(), "ws-2", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestUnset_RemovesLinkAndIndexEntry(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))

	require.NoError(t, svc.Unset(context.Background(), "task-1"))

	_, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = svc.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestUnset_NotLinked_IsNoOp(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Unset(context.Background(), "task-1"))
}

func TestSetEchoSuppression_RoundTrips(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))

	statusID := 5
	require.NoError(t, svc.RecordPushedStatus(context.Background(), "task-1", statusID))

	link, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, link.LastPushedStatusID)
	require.Equal(t, 5, *link.LastPushedStatusID)
}

func TestRecordPushedTitleAndDescription_RoundTrips(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))

	require.NoError(t, svc.RecordPushedTitleAndDescription(context.Background(), "task-1", "New title", "New description"))

	link, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "New title", link.LastPushedTitle)
	require.NotEmpty(t, link.LastPushedDescriptionHash)
}

func TestSet_RelinkRemovesOldIndexAndRejectsDuplicateIssue(t *testing.T) {
	svc := New(newFakeHost())
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 43, "url"))
	_, found, err := svc.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.False(t, found)

	require.Error(t, svc.Set(context.Background(), "task-2", "ws-1", 43, "url"))
	taskID, found, err := svc.TaskIDForIssue(context.Background(), "ws-1", 43)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "task-1", taskID)
}

func TestSet_RelinkIndexFailuresRestoreForwardAndReverseLinks(t *testing.T) {
	for _, targetWorkspace := range []string{"ws-1", "ws-2"} {
		t.Run(targetWorkspace, func(t *testing.T) {
			host := newFakeHost()
			svc := New(host)
			require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))
			if targetWorkspace == "ws-1" {
				host.failNextSet(workspaceScope, "ws-1", indexKey)
			} else {
				host.failNextSet(workspaceScope, "ws-2", indexKey)
			}
			require.Error(t, svc.Set(context.Background(), "task-1", targetWorkspace, 43, "url"))
			link, found, err := svc.Get(context.Background(), "task-1")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "ws-1", link.WorkspaceID)
			require.Equal(t, 42, link.IssueID)
			taskID, found, err := svc.TaskIDForIssue(context.Background(), "ws-1", 42)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "task-1", taskID)
			_, found, err = svc.TaskIDForIssue(context.Background(), targetWorkspace, 43)
			require.NoError(t, err)
			require.False(t, found)
		})
	}
}

func TestUnset_IndexFailureRestoresLinkAndReverseIndex(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	require.NoError(t, svc.Set(context.Background(), "task-1", "ws-1", 42, "url"))
	host.failNextSet(workspaceScope, "ws-1", indexKey)
	require.Error(t, svc.Unset(context.Background(), "task-1"))
	link, found, err := svc.Get(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 42, link.IssueID)
	taskID, found, err := svc.TaskIDForIssue(context.Background(), "ws-1", 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "task-1", taskID)
}
