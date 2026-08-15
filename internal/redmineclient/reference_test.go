package redmineclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func TestListProjectsPage_SendsOffsetAndLimit(t *testing.T) {
	var gotOffset, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOffset = r.URL.Query().Get("offset")
		gotLimit = r.URL.Query().Get("limit")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":1,"name":"Demo","identifier":"demo"}],"total_count":1,"offset":0,"limit":100}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "key", srv.Client())
	items, total, err := c.ListProjectsPage(context.Background(), 0, 100)
	require.NoError(t, err)
	require.Equal(t, "0", gotOffset)
	require.Equal(t, "100", gotLimit)
	require.Equal(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, "Demo", items[0].Name)
}

func TestListIssueStatuses_ReturnsIsClosedFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/issue_statuses.json", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issue_statuses":[{"id":1,"name":"Triage","is_closed":false},{"id":2,"name":"Shipped","is_closed":true}]}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "key", srv.Client())
	statuses, err := c.ListIssueStatuses(context.Background())
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	require.False(t, statuses[0].IsClosed)
	require.True(t, statuses[1].IsClosed)
}

func TestListTrackers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/trackers.json", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"trackers":[{"id":1,"name":"Bug"},{"id":2,"name":"Feature"}]}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "key", srv.Client())
	trackers, err := c.ListTrackers(context.Background())
	require.NoError(t, err)
	require.Len(t, trackers, 2)
	require.Equal(t, "Bug", trackers[0].Name)
}

func TestListIssuePriorities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/enumerations/issue_priorities.json", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issue_priorities":[{"id":1,"name":"Urgent"}]}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "key", srv.Client())
	priorities, err := c.ListIssuePriorities(context.Background())
	require.NoError(t, err)
	require.Len(t, priorities, 1)
	require.Equal(t, "Urgent", priorities[0].Name)
}

func TestListCustomFields_AdminKey_ReturnsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/custom_fields.json", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"custom_fields":[{"id":5,"name":"Severity"}]}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "admin-key", srv.Client())
	fields, err := c.ListCustomFields(context.Background())
	require.NoError(t, err)
	require.Len(t, fields, 1)
	require.Equal(t, "Severity", fields[0].Name)
}

func TestListCustomFields_NonAdminKey_ReturnsAPIDisabledKindError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "non-admin-key", srv.Client())
	_, err := c.ListCustomFields(context.Background())
	require.Error(t, err)
	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindAPIDisabled, apiErr.Kind)
}
