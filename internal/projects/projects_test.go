package projects

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func TestListLive_WalksAllPagesUntilTotalCountReached(t *testing.T) {
	// A 250-project instance: pages of 100, 100, 50 at limit=100.
	var requestedOffsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		requestedOffsets = append(requestedOffsets, offset)
		offsetInt, _ := strconv.Atoi(offset)

		pageSize := map[string]int{"0": 100, "100": 100, "200": 50}[offset]
		var items []string
		for i := 0; i < pageSize; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"p"}`, offsetInt+i+1))
		}
		body := fmt.Sprintf(`{"projects":[%s],"total_count":250,"offset":%s,"limit":100}`, strings.Join(items, ","), offset)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := redmineclient.New(srv.URL, "key", srv.Client())
	svc := New(newFakeHost())

	all, err := svc.ListLive(context.Background(), client)
	require.NoError(t, err)
	require.Len(t, all, 250)
	require.Equal(t, []string{"0", "100", "200"}, requestedOffsets)
}

func TestListLive_EmptyInstance_ReturnsNoProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[],"total_count":0,"offset":0,"limit":100}`))
	}))
	defer srv.Close()

	client := redmineclient.New(srv.URL, "key", srv.Client())
	svc := New(newFakeHost())

	all, err := svc.ListLive(context.Background(), client)
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestSaveAndGetSelection_RoundTrips(t *testing.T) {
	svc := New(newFakeHost())

	require.NoError(t, svc.SaveSelection(context.Background(), "ws-1", []int{3, 1, 2}))

	got, err := svc.GetSelection(context.Background(), "ws-1")
	require.NoError(t, err)
	require.ElementsMatch(t, []int{1, 2, 3}, got)
}

func TestGetSelection_NoneSaved_ReturnsEmpty(t *testing.T) {
	svc := New(newFakeHost())
	got, err := svc.GetSelection(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSaveSelection_TwoWorkspaces_DoNotShareSelection(t *testing.T) {
	host := newFakeHost()
	svc := New(host)

	require.NoError(t, svc.SaveSelection(context.Background(), "ws-1", []int{1}))
	require.NoError(t, svc.SaveSelection(context.Background(), "ws-2", []int{2}))

	ws1, err := svc.GetSelection(context.Background(), "ws-1")
	require.NoError(t, err)
	require.Equal(t, []int{1}, ws1)

	ws2, err := svc.GetSelection(context.Background(), "ws-2")
	require.NoError(t, err)
	require.Equal(t, []int{2}, ws2)
}
