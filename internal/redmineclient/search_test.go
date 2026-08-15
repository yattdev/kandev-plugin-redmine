package redmineclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func TestSearchIssues_SendsQueryAndIssuesOnlyFilter(t *testing.T) {
	var gotQuery, gotIssues string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search.json", r.URL.Path)
		gotQuery = r.URL.Query().Get("q")
		gotIssues = r.URL.Query().Get("issues")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"id":42,"title":"Bug: crash on login","type":"issue","url":"https://redmine.example/issues/42"}]}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "key", srv.Client())
	results, err := c.SearchIssues(context.Background(), "crash", 10)
	require.NoError(t, err)
	require.Equal(t, "crash", gotQuery)
	require.Equal(t, "1", gotIssues)
	require.Len(t, results, 1)
	require.Equal(t, 42, results[0].ID)
	require.Equal(t, "Bug: crash on login", results[0].Title)
}
