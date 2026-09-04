package redmineclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func TestValidateCredentials_ValidKey_ReturnsUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/users/current.json", r.URL.Path)
		require.Equal(t, "s3cret", r.Header.Get("X-Redmine-API-Key"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1,"login":"alice","firstname":"Alice","lastname":"Anderson","admin":true}}`))
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "s3cret", srv.Client())
	user, err := c.ValidateCredentials(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, user.ID)
	require.Equal(t, "alice", user.Login)
	require.True(t, user.Admin)
}

func TestValidateCredentials_InvalidKey_ReturnsDistinctError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "bad-key", srv.Client())
	_, err := c.ValidateCredentials(context.Background())
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindInvalidCredentials, apiErr.Kind)
	require.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestValidateCredentials_APIDisabled_ReturnsDistinctError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "s3cret", srv.Client())
	_, err := c.ValidateCredentials(context.Background())
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindAPIDisabled, apiErr.Kind)
}

func TestEndpointForbidden_IsPermissionDeniedNotAPIDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	c := redmineclient.New(srv.URL, "key", srv.Client())
	var out map[string]any
	err := c.GetJSON(context.Background(), "/custom_fields.json", nil, &out)
	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindPermissionDenied, apiErr.Kind)
}

func TestValidateCredentials_Subpath403IsAPIDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/redmine/users/current.json", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := redmineclient.New(srv.URL+"/redmine", "key", srv.Client())
	_, err := c.ValidateCredentials(context.Background())
	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindAPIDisabled, apiErr.Kind)
}

func TestValidateCredentials_Unreachable_ReturnsDistinctError(t *testing.T) {
	// A closed listener: connection refused, never reaches the fake server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := srv.URL
	srv.Close()

	c := redmineclient.New(unreachableURL, "s3cret", http.DefaultClient)
	_, err := c.ValidateCredentials(context.Background())
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindUnreachable, apiErr.Kind)
}

func TestValidateCredentials_UnexpectedStatus_ReturnsDistinctError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := redmineclient.New(srv.URL, "s3cret", srv.Client())
	_, err := c.ValidateCredentials(context.Background())
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindUnexpected, apiErr.Kind)
	require.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
}

func TestNormalizeBaseURL_RejectsUnsafeFormsAndPreservesSubpath(t *testing.T) {
	got, err := redmineclient.NormalizeBaseURL(" https://redmine.example/redmine/ ")
	require.NoError(t, err)
	require.Equal(t, "https://redmine.example/redmine", got)
	got, err = redmineclient.NormalizeBaseURL("https://redmine.example/redmine%20space/")
	require.NoError(t, err)
	require.Equal(t, "https://redmine.example/redmine%20space", got)
	for _, raw := range []string{"ftp://redmine.example", "https://user:pass@redmine.example", "https://redmine.example/?x=1", "https://redmine.example/#fragment", "/relative"} {
		_, err := redmineclient.NormalizeBaseURL(raw)
		require.Error(t, err, raw)
	}
}

func TestDefaultClient_RefusesCrossOriginRedirectWithoutFollowingIt(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit = true }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirector.Close()

	c := redmineclient.New(redirector.URL, "secret", nil)
	_, err := c.ValidateCredentials(context.Background())
	require.Error(t, err)
	require.False(t, targetHit)
}

func TestGetRetriesTransientFailureButPostDoesNotDuplicate(t *testing.T) {
	getCalls, postCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalls++
			if getCalls < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"user":{"id":1}}`))
		case http.MethodPost:
			postCalls++
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	c := redmineclient.New(srv.URL, "key", srv.Client())
	_, err := c.ValidateCredentials(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, getCalls)
	err = c.PostJSON(context.Background(), "/issues.json", map[string]string{"subject": "one"}, nil)
	require.Error(t, err)
	require.Equal(t, 1, postCalls)
}
