package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestSearchEntityReferences_WrongSource_ReturnsNoCandidates(t *testing.T) {
	p, _ := newTestPlugin(t)
	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: "something-else"})
	require.NoError(t, err)
	require.Empty(t, resp.Candidates)
}

func TestSearchEntityReferences_NotConnected_ReturnsNoCandidates(t *testing.T) {
	p, _ := newTestPlugin(t)
	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: referenceSource, WorkspaceID: "ws-1", Query: "bug"})
	require.NoError(t, err)
	require.Empty(t, resp.Candidates)
}

func TestSearchEntityReferences_Connected_ReturnsCandidates(t *testing.T) {
	p, _ := newTestPlugin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/search.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"id":42,"title":"Bug: crash","url":"https://redmine.example/issues/42"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1}))

	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: referenceSource, WorkspaceID: "ws-1", Query: "crash"})
	require.NoError(t, err)
	require.Len(t, resp.Candidates, 1)
	require.Equal(t, "42", resp.Candidates[0].ProviderLocalID)
}

func TestSearchEntityReferences_ScopesProjectsDedupesAndAppliesGlobalLimit(t *testing.T) {
	p, _ := newTestPlugin(t)
	var projectIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/search.json":
			projectID := r.URL.Query().Get("project_id")
			projectIDs = append(projectIDs, projectID)
			if projectID == "1" {
				_, _ = w.Write([]byte(`{"results":[{"id":42,"title":"One","url":"https://redmine.example/issues/42"},{"id":99,"title":"Two","url":"https://redmine.example/issues/99"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"id":42,"title":"Duplicate","url":"https://redmine.example/issues/42"},{"id":100,"title":"Three","url":"https://redmine.example/issues/100"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1, 2}))

	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: referenceSource, WorkspaceID: "ws-1", Query: "bug", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"1", "2"}, projectIDs)
	require.Len(t, resp.Candidates, 2)
	require.Equal(t, "42", resp.Candidates[0].ProviderLocalID)
	require.Equal(t, "99", resp.Candidates[1].ProviderLocalID)
}

func TestAuthorizeEntityReference_SubmissionPurpose_ReVerifiesIssueExists(t *testing.T) {
	p, _ := newTestPlugin(t)
	var issueExists bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/current.json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/issues/42.json":
			if !issueExists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"still here","project":{"id":1}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1}))

	// Not yet available: submission-time authorization must refuse.
	resp, err := p.AuthorizeEntityReference(context.Background(), &pluginsdk.AuthorizeEntityReferenceRequest{
		Source: referenceSource, WorkspaceID: "ws-1", Purpose: referencePurposeSubmission,
		Reference: map[string]any{"id": "42"},
	})
	require.NoError(t, err)
	require.False(t, resp.Allowed)

	// Now it exists: submission-time authorization must allow.
	issueExists = true
	resp, err = p.AuthorizeEntityReference(context.Background(), &pluginsdk.AuthorizeEntityReferenceRequest{
		Source: referenceSource, WorkspaceID: "ws-1", Purpose: referencePurposeSubmission,
		Reference: map[string]any{"id": "42"},
	})
	require.NoError(t, err)
	require.True(t, resp.Allowed)
}

func TestAuthorizeEntityReference_SubmissionRejectsUnselectedAndUntrustedReferences(t *testing.T) {
	p, _ := newTestPlugin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redmine/users/current.json":
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case "/redmine/issues/42.json":
			_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"outside","project":{"id":2}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL + "/redmine", "api_key": "good-key"})
	require.NoError(t, p.projectsSvc.SaveSelection(context.Background(), "ws-1", []int{1}))
	for _, id := range []any{"abc42", "0", "999999999999999999999999", "https://other.example/redmine/issues/42", srv.URL + "/issues/42"} {
		resp, err := p.AuthorizeEntityReference(context.Background(), &pluginsdk.AuthorizeEntityReferenceRequest{Source: referenceSource, WorkspaceID: "ws-1", Purpose: referencePurposeSubmission, Reference: map[string]any{"id": id}})
		require.NoError(t, err)
		require.False(t, resp.Allowed, id)
	}
	resp, err := p.AuthorizeEntityReference(context.Background(), &pluginsdk.AuthorizeEntityReferenceRequest{Source: referenceSource, WorkspaceID: "ws-1", Purpose: referencePurposeSubmission, Reference: map[string]any{"id": "42"}})
	require.NoError(t, err)
	require.False(t, resp.Allowed, "the existing issue belongs to an unselected project")
}

func TestAuthorizeEntityReference_SearchPurpose_AllowsWithoutReverify(t *testing.T) {
	p, _ := newTestPlugin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})

	resp, err := p.AuthorizeEntityReference(context.Background(), &pluginsdk.AuthorizeEntityReferenceRequest{
		Source: referenceSource, WorkspaceID: "ws-1", Purpose: referencePurposeSearch,
		Reference: map[string]any{"id": "42"},
	})
	require.NoError(t, err)
	require.True(t, resp.Allowed)
}
