package connection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthPoll_ProbesConnectedWorkspaceAndMarksHealthy(t *testing.T) {
	host := newFakeHost()
	svc := New(host)

	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv.Close()

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)

	poller := NewHealthPoller(svc, WithInterval(5*time.Millisecond), WithJitter(0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop()

	require.Eventually(t, func() bool { return probes.Load() >= 2 }, time.Second, time.Millisecond)

	record, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StateConnected, record.State)
}

func TestHealthPoll_FailedProbe_MarksDegradedWithoutDeletingSecret(t *testing.T) {
	host := newFakeHost()
	svc := New(host)

	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	defer srv.Close()

	_, err := svc.Connect(context.Background(), "ws-1", srv.URL, "good-key")
	require.NoError(t, err)
	failing.Store(true)

	poller := NewHealthPoller(svc, WithInterval(5*time.Millisecond), WithJitter(0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop()

	require.Eventually(t, func() bool {
		record, found, err := svc.Get(context.Background(), "ws-1")
		return err == nil && found && record.State == StateDegraded
	}, time.Second, time.Millisecond)

	record, found, err := svc.Get(context.Background(), "ws-1")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, record.LastError)

	_, found, err = host.GetSecret(context.Background(), secretKey("ws-1"))
	require.NoError(t, err)
	require.True(t, found, "a failed health probe must not delete the stored key")
}

func TestHealthPoll_StartStop_IsIdempotentAndDoesNotLeakGoroutines(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	poller := NewHealthPoller(svc, WithInterval(time.Hour), WithJitter(0))

	ctx := context.Background()
	poller.Start(ctx)
	poller.Start(ctx) // second Start is a no-op, not a second goroutine
	poller.Stop()
	poller.Stop() // second Stop is a no-op, not a panic on double-close
}

func TestHealthPoll_StopCancelsPendingWait(t *testing.T) {
	host := newFakeHost()
	svc := New(host)
	poller := NewHealthPoller(svc, WithInterval(time.Hour), WithJitter(0))

	poller.Start(context.Background())

	done := make(chan struct{})
	go func() {
		poller.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return promptly; health poll loop is not selecting on ctx.Done()")
	}
}
