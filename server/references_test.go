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
	p, _ := newTestPlugin()
	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: "something-else"})
	require.NoError(t, err)
	require.Empty(t, resp.Candidates)
}

func TestSearchEntityReferences_NotConnected_ReturnsNoCandidates(t *testing.T) {
	p, _ := newTestPlugin()
	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: referenceSource, WorkspaceID: "ws-1", Query: "bug"})
	require.NoError(t, err)
	require.Empty(t, resp.Candidates)
}

func TestSearchEntityReferences_Connected_ReturnsCandidates(t *testing.T) {
	p, _ := newTestPlugin()
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

	resp, err := p.SearchEntityReferences(context.Background(), &pluginsdk.SearchEntityReferencesRequest{Source: referenceSource, WorkspaceID: "ws-1", Query: "crash"})
	require.NoError(t, err)
	require.Len(t, resp.Candidates, 1)
	require.Equal(t, "42", resp.Candidates[0].ProviderLocalID)
}

func TestAuthorizeEntityReference_SubmissionPurpose_ReVerifiesIssueExists(t *testing.T) {
	p, _ := newTestPlugin()
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
			_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"still here"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	handle(t, p, "connection.save", "ws-1", "", map[string]any{"base_url": srv.URL, "api_key": "good-key"})

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

func TestAuthorizeEntityReference_SearchPurpose_AllowsWithoutReverify(t *testing.T) {
	p, _ := newTestPlugin()
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
