package connection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kandev-plugin-redmine/internal/redmineclient"
	"kandev-plugin-redmine/internal/secretcrypto"

	"github.com/stretchr/testify/require"
)

func validRedmineServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Redmine-API-Key") != "good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1,"login":"alice"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConnect_ValidKey_PersistsHostManagedSecretAndConnectedState(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)

	record, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)
	require.Equal(t, StateConnected, record.State)
	require.Equal(t, srv.URL, record.BaseURL)
	require.NotEmpty(t, record.LastOK)

	stored, found, err := host.GetSecret(context.Background(), secretKey("ws-1"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "good-key", stored, "plugin hands plaintext only to the host secret store")

	got, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StateConnected, got.State)
}

func TestConnect_InvalidKey_PersistsNothing(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "bad-key")
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindInvalidCredentials, apiErr.Kind)

	_, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = host.GetSecret(context.Background(), secretKey("ws-1"))
	require.NoError(t, err)
	require.False(t, found)
}

func TestConnect_APIDisabled_ReturnsDistinctError(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "any-key")
	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindAPIDisabled, apiErr.Kind)
}

func TestConnect_UnreachableHost_DoesNotOverwritePreviousHealthyState(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)

	unreachableURL := "http://127.0.0.1:1" // nothing listens here
	_, err = svc.Connect(context.Background(), "ws-1", unreachableURL, "good-key")
	require.Error(t, err)

	var apiErr *redmineclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, redmineclient.ErrKindUnreachable, apiErr.Kind)

	got, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StateConnected, got.State) // unchanged from the first successful Connect
	require.Equal(t, srv.URL, got.BaseURL)
}

func TestConnect_RotatingKey_ReplacesHostManagedSecretUnderSameKey(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Redmine-API-Key")
		if key != "first-key" && key != "second-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv.Close()

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "first-key")
	require.NoError(t, err)
	firstSecret, _, _ := host.GetSecret(context.Background(), secretKey("ws-1"))

	_, err = svc.Connect(context.Background(), "ws-1", srv.URL, "second-key")
	require.NoError(t, err)
	secondSecret, _, _ := host.GetSecret(context.Background(), secretKey("ws-1"))

	require.Equal(t, "first-key", firstSecret)
	require.Equal(t, "second-key", secondSecret)
}

func TestDisconnect_RemovesSecretAndState(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)

	require.NoError(t, svc.Disconnect(context.Background(), "ws-1"))

	_, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = host.GetSecret(context.Background(), secretKey("ws-1"))
	require.NoError(t, err)
	require.False(t, found)

	ids, err := svc.ListWorkspaceIDs(context.Background())
	require.NoError(t, err)
	require.NotContains(t, ids, "ws-1")
}

func TestClient_ResolvesAuthenticatedClientFromStoredConnection(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)

	client, err := svc.Client(context.Background(), "ws-1")
	require.NoError(t, err)
	user, err := client.ValidateCredentials(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, user.ID)
}

func TestClient_NoConnection_Errors(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	_, err := svc.Client(context.Background(), "ws-1")
	require.Error(t, err)
}

func TestTwoWorkspaces_CannotReadOrAffectEachOthersSecretOrState(t *testing.T) {
	host := newFakeHost()
	svc := New(host)

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":2}}`))
	}))
	defer srv2.Close()

	_, err := svc.Connect(context.Background(), "ws-1", srv1.URL, "ws1-key")
	require.NoError(t, err)
	_, err = svc.Connect(context.Background(), "ws-2", srv2.URL, "ws2-key")
	require.NoError(t, err)

	ws1Secret, _, _ := host.GetSecret(context.Background(), secretKey("ws-1"))
	ws2Secret, _, _ := host.GetSecret(context.Background(), secretKey("ws-2"))
	require.NotEqual(t, ws1Secret, ws2Secret)

	require.Equal(t, "ws1-key", ws1Secret)
	require.Equal(t, "ws2-key", ws2Secret)

	require.NoError(t, svc.Disconnect(context.Background(), "ws-1"))

	got2, found, err := svc.Get(context.Background(), "ws-2")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StateConnected, got2.State)

	_, found, err = host.GetSecret(context.Background(), secretKey("ws-2"))
	require.NoError(t, err)
	require.True(t, found, "disconnecting ws-1 must not remove ws-2's secret")
}

func TestClient_LegacyEncryptedSecretMigratesToHostManagedValue(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	srv := validRedmineServer(t)
	legacy, err := secretcrypto.Encrypt("ws-1", "good-key")
	require.NoError(t, err)
	require.NoError(t, host.SetSecret(context.Background(), secretKey("ws-1"), legacy))
	require.NoError(t, svc.saveRecord(context.Background(), "ws-1", &Record{BaseURL: srv.URL, State: StateConnected}))

	client, err := svc.Client(context.Background(), "ws-1")
	require.NoError(t, err)
	_, err = client.ValidateCredentials(context.Background())
	require.NoError(t, err)
	stored, found, err := host.GetSecret(context.Background(), secretKey("ws-1"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "good-key", stored)
}
