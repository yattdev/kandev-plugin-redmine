package fieldmapping

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestService_SaveAndGet_RoundTrips(t *testing.T) {
	svc := New(newFakeHost())
	mapping := Mapping{
		Statuses: []StatusMapping{
			{RedmineStatusID: 2, RedmineName: "Shipped", IsClosed: true, WorkflowStepID: "step-done"},
		},
		Trackers: []TrackerMapping{
			{RedmineTrackerID: 1, RedmineName: "Defect", TaskLabel: "bug"},
		},
		Priorities: []PriorityMapping{
			{RedminePriorityID: 1, RedmineName: "Urgent", TaskPriority: "critical"},
		},
	}

	require.NoError(t, svc.Save(context.Background(), "ws-1", mapping))

	got, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, mapping, got)
}

func TestGet_NoneSaved_ReturnsNotFound(t *testing.T) {
	svc := New(newFakeHost())
	_, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.False(t, found)
}

func TestGet_CapturesIsClosedFlagPerStatus(t *testing.T) {
	svc := New(newFakeHost())
	mapping := Mapping{Statuses: []StatusMapping{
		{RedmineStatusID: 1, RedmineName: "Triage", IsClosed: false, WorkflowStepID: "step-a"},
		{RedmineStatusID: 2, RedmineName: "Archived", IsClosed: true, WorkflowStepID: "step-b"},
	}}
	require.NoError(t, svc.Save(context.Background(), "ws-1", mapping))

	got, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, got.Statuses[0].IsClosed)
	require.True(t, got.Statuses[1].IsClosed)
}

func TestDeriveCustomFieldsFromIssues_UnionsAndDedupsByID(t *testing.T) {
	fields := DeriveCustomFieldsFromIssues([][]CustomField{
		{{ID: 1, Name: "Severity"}, {ID: 2, Name: "Team"}},
		{{ID: 2, Name: "Team"}, {ID: 3, Name: "Component"}},
	})
	require.Len(t, fields, 3)

	ids := make([]int, len(fields))
	for i, f := range fields {
		ids[i] = f.ID
	}
	require.ElementsMatch(t, []int{1, 2, 3}, ids)
}

func TestDeriveCustomFieldsFromIssues_NoIssues_ReturnsEmpty(t *testing.T) {
	fields := DeriveCustomFieldsFromIssues(nil)
	require.Empty(t, fields)
}
