// Package connection owns the Redmine connection lifecycle: validating a
// base URL + API key, persisting the encrypted key and connection metadata,
// and the workspace index the health poller (healthpoll.go) walks. One
// Service instance is shared by every workspace; every method takes the
// caller-supplied workspaceID and derives its own plugin_state/secret keys
// from it — never from anything else — so two workspaces' connections never
// read or affect each other (spec "Connection, permissions, and secrets").
package connection

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"kandev-plugin-redmine/internal/redmineclient"
	"kandev-plugin-redmine/internal/secretcrypto"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// State is the connection lifecycle state from the spec's "Connection,
// permissions, and secrets" section.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateDegraded     State = "degraded"
)

// Record is the non-secret connection metadata persisted in plugin_state.
type Record struct {
	BaseURL   string
	State     State
	LastOK    string
	LastError string
}

const (
	stateScope    = "workspace"
	stateKey      = "connection"
	indexScope    = "instance"
	indexScopeID  = ""
	indexStateKey = "workspaces"
)

// secretKey composes the workspace ID into the secret key itself, since the
// host's GetSecret/SetSecret/DeleteSecret RPCs are namespaced only by plugin
// ID, not by workspace. Dots, not colons, per pluginSecretKeyPattern
// (^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$ — colons are rejected).
func secretKey(workspaceID string) string {
	return fmt.Sprintf("redmine.%s.api_key", workspaceID)
}

// Service is the connection lifecycle service. Safe for concurrent use: it
// holds no mutable state of its own, only a Host handle and an HTTP client.
type Service struct {
	host       pluginsdk.Host
	httpClient *http.Client
}

func New(host pluginsdk.Host) *Service {
	return &Service{host: host}
}

// Connect validates baseURL+apiKey against GET /users/current.json. On
// success it encrypts and persists the key plus StateConnected metadata and
// returns the new Record. On failure — invalid credentials, API disabled, or
// unreachable host, each a distinct *redmineclient.APIError via errors.As —
// nothing is persisted and any previously stored connection is left
// unchanged. Also used for key rotation: a repeat Connect call for the same
// workspace replaces the stored ciphertext under the same composed key.
func (s *Service) Connect(ctx context.Context, workspaceID, baseURL, apiKey string) (*Record, error) {
	if workspaceID == "" {
		return nil, errors.New("connection: workspace id is required")
	}
	if baseURL == "" || apiKey == "" {
		return nil, errors.New("connection: base url and api key are required")
	}
	normalizedURL, err := redmineclient.NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	client := redmineclient.New(normalizedURL, apiKey, s.httpClient)
	if _, err := client.ValidateCredentials(ctx); err != nil {
		return nil, err
	}

	encrypted, err := secretcrypto.Encrypt(workspaceID, apiKey)
	if err != nil {
		return nil, fmt.Errorf("connection: encrypting api key: %w", err)
	}
	if err := s.host.SetSecret(ctx, secretKey(workspaceID), encrypted); err != nil {
		return nil, fmt.Errorf("connection: storing api key: %w", err)
	}

	record := &Record{BaseURL: normalizedURL, State: StateConnected, LastOK: nowRFC3339()}
	if err := s.saveRecord(ctx, workspaceID, record); err != nil {
		return nil, err
	}
	if err := s.addToIndex(ctx, workspaceID); err != nil {
		return nil, err
	}
	return record, nil
}

// Get returns the current connection Record for workspaceID, if any.
func (s *Service) Get(ctx context.Context, workspaceID string) (*Record, bool, error) {
	value, found, err := s.host.GetState(ctx, stateScope, workspaceID, stateKey)
	if err != nil {
		return nil, false, fmt.Errorf("connection: reading state: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	return recordFromMap(value), true, nil
}

// Client resolves an authenticated *redmineclient.Client for workspaceID
// from its stored connection record and decrypted secret — the shared way
// every other package (issues, projects, fieldmapping, sync, watch) reaches
// Redmine for a given workspace.
func (s *Service) Client(ctx context.Context, workspaceID string) (*redmineclient.Client, error) {
	record, found, err := s.Get(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("connection: workspace %s has no connection", workspaceID)
	}
	apiKey, err := s.decryptedAPIKey(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return redmineclient.New(record.BaseURL, apiKey, s.httpClient), nil
}

// Disconnect removes both the encrypted secret and the connection state for
// workspaceID and stops it from being health-polled.
func (s *Service) Disconnect(ctx context.Context, workspaceID string) error {
	if err := s.host.DeleteSecret(ctx, secretKey(workspaceID)); err != nil {
		return fmt.Errorf("connection: deleting secret: %w", err)
	}
	if err := s.host.DeleteState(ctx, stateScope, workspaceID, stateKey); err != nil {
		return fmt.Errorf("connection: deleting state: %w", err)
	}
	return s.removeFromIndex(ctx, workspaceID)
}

// ListWorkspaceIDs returns every workspace with a connection, for the health
// poller to walk. The host's ListState only lists keys within one
// scope+scopeID, so this plugin maintains its own instance-scoped index.
func (s *Service) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	value, found, err := s.host.GetState(ctx, indexScope, indexScopeID, indexStateKey)
	if err != nil {
		return nil, fmt.Errorf("connection: reading workspace index: %w", err)
	}
	if !found {
		return nil, nil
	}
	raw, _ := value["workspace_ids"].([]any)
	ids := make([]string, 0, len(raw))
	for _, v := range raw {
		if id, ok := v.(string); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Service) addToIndex(ctx context.Context, workspaceID string) error {
	ids, err := s.ListWorkspaceIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == workspaceID {
			return nil
		}
	}
	return s.saveIndex(ctx, append(ids, workspaceID))
}

func (s *Service) removeFromIndex(ctx context.Context, workspaceID string) error {
	ids, err := s.ListWorkspaceIDs(ctx)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != workspaceID {
			kept = append(kept, id)
		}
	}
	return s.saveIndex(ctx, kept)
}

func (s *Service) saveIndex(ctx context.Context, ids []string) error {
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id
	}
	if err := s.host.SetState(ctx, indexScope, indexScopeID, indexStateKey, map[string]any{"workspace_ids": values}); err != nil {
		return fmt.Errorf("connection: saving workspace index: %w", err)
	}
	return nil
}

func (s *Service) saveRecord(ctx context.Context, workspaceID string, record *Record) error {
	if err := s.host.SetState(ctx, stateScope, workspaceID, stateKey, record.toMap()); err != nil {
		return fmt.Errorf("connection: saving state: %w", err)
	}
	return nil
}

// markHealthy and markDegraded are used by the health poller (healthpoll.go)
// to flip state without touching the stored secret — an invalid/unreachable
// probe never deletes the key (spec Failure modes).
func (s *Service) markHealthy(ctx context.Context, workspaceID string, record *Record) error {
	record.State = StateConnected
	record.LastOK = nowRFC3339()
	record.LastError = ""
	return s.saveRecord(ctx, workspaceID, record)
}

func (s *Service) markDegraded(ctx context.Context, workspaceID string, record *Record, reason string) error {
	record.State = StateDegraded
	record.LastError = reason
	return s.saveRecord(ctx, workspaceID, record)
}

// decryptedAPIKey resolves and decrypts the stored API key for workspaceID.
func (s *Service) decryptedAPIKey(ctx context.Context, workspaceID string) (string, error) {
	encrypted, found, err := s.host.GetSecret(ctx, secretKey(workspaceID))
	if err != nil {
		return "", fmt.Errorf("connection: reading secret: %w", err)
	}
	if !found {
		return "", errors.New("connection: no api key stored for workspace")
	}
	return secretcrypto.Decrypt(workspaceID, encrypted)
}

func (r *Record) toMap() map[string]any {
	m := map[string]any{
		"base_url": r.BaseURL,
		"state":    string(r.State),
	}
	if r.LastOK != "" {
		m["last_ok"] = r.LastOK
	}
	if r.LastError != "" {
		m["last_error"] = r.LastError
	}
	return m
}

func recordFromMap(m map[string]any) *Record {
	r := &Record{}
	if v, ok := m["base_url"].(string); ok {
		r.BaseURL = v
	}
	if v, ok := m["state"].(string); ok {
		r.State = State(v)
	}
	if v, ok := m["last_ok"].(string); ok {
		r.LastOK = v
	}
	if v, ok := m["last_error"].(string); ok {
		r.LastError = v
	}
	return r
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
