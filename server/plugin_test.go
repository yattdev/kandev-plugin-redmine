// Package main tests. Exercises redminePlugin's inherited Plugin behavior
// against a fake Host — no go-plugin subprocess needed. Grows alongside
// plugin.go as later plan tasks add ActionHandler, EntityReferenceSearcher,
// and PluginOwnedTaskTrees usage.
package main

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

// fakeHost is a minimal pluginsdk.Host test double. UnimplementedHostData
// satisfies the data-reader accessors this test doesn't exercise; the core
// state/secret/config/event methods have no unimplemented default and are
// stubbed here directly.
type fakeHost struct {
	pluginsdk.UnimplementedHostData
}

func (*fakeHost) GetState(context.Context, string, string, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (*fakeHost) SetState(context.Context, string, string, string, map[string]any) error { return nil }
func (*fakeHost) DeleteState(context.Context, string, string, string) error              { return nil }
func (*fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}
func (*fakeHost) GetConfig(context.Context) (map[string]any, error)    { return map[string]any{}, nil }
func (*fakeHost) RevealSecret(context.Context, string) (string, error) { return "", nil }
func (*fakeHost) GetSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (*fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (*fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (*fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

var _ pluginsdk.Host = (*fakeHost)(nil)

func TestRedminePlugin_HostRoundTrip(t *testing.T) {
	p := &redminePlugin{}
	require.Nil(t, p.Host())

	host := &fakeHost{}
	p.SetHost(host)
	require.Same(t, pluginsdk.Host(host), p.Host())
}

func TestRedminePlugin_OnEvent_NoOpWithoutOverride(t *testing.T) {
	p := &redminePlugin{}
	err := p.OnEvent(context.Background(), &pluginsdk.Event{EventID: "e1", EventType: "task.created"})
	require.NoError(t, err)
}

func TestRedminePlugin_HandleWebhook_UnimplementedWithoutOverride(t *testing.T) {
	p := &redminePlugin{}
	resp, err := p.HandleWebhook(context.Background(), &pluginsdk.WebhookRequest{WebhookKey: "ping", Method: "POST"})
	require.NoError(t, err)
	require.Equal(t, int32(404), resp.Status)
}
