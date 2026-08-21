package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"kandev-plugin-redmine/internal/connection"
	"kandev-plugin-redmine/internal/fieldmapping"
	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"
	redminesync "kandev-plugin-redmine/internal/sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// asAPIError unwraps err into a *redmineclient.APIError, if it is (or
// wraps) one.
func asAPIError(err error) (*redmineclient.APIError, bool) {
	var apiErr *redmineclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// HandleAction answers every actions: entry in manifest.yaml — the plugin's
// own native settings UI calling into this backend via
// host.api.invokeAction. req.Context is host-verified (workspace/task
// membership already checked); every handler derives its plugin_state/secret
// keys from req.Context, never from req.Body, so a forged body can't target
// another workspace's connection or another task's link.
func (p *redminePlugin) HandleAction(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("redmine: missing action request")
	}
	switch req.ActionKey {
	case "connection.get":
		return p.handleConnectionGet(ctx, req)
	case "connection.save":
		return p.handleConnectionSave(ctx, req)
	case "connection.disconnect":
		return p.handleConnectionDisconnect(ctx, req)
	case "projects.list":
		return p.handleProjectsList(ctx, req)
	case "projects.save":
		return p.handleProjectsSave(ctx, req)
	case "workflows.list":
		return p.handleWorkflowsList(ctx, req)
	case "fieldmapping.get":
		return p.handleFieldMappingGet(ctx, req)
	case "fieldmapping.save":
		return p.handleFieldMappingSave(ctx, req)
	case "syncoptions.save":
		return p.handleSyncOptionsSave(ctx, req)
	case "issues.create":
		return p.handleIssueCreate(ctx, req)
	case "issues.update":
		return p.handleIssueUpdate(ctx, req)
	case "issues.upload":
		return p.handleIssueUpload(ctx, req)
	case "link.get":
		return p.handleLinkGet(ctx, req)
	case "link.set":
		return p.handleLinkSet(ctx, req)
	case "link.unset":
		return p.handleLinkUnset(ctx, req)
	case "link.set_status":
		return p.handleLinkSetStatus(ctx, req)
	case "watches.list":
		return p.handleWatchesList(ctx, req)
	case "watches.create":
		return p.handleWatchesCreate(ctx, req)
	case "watches.update":
		return p.handleWatchesUpdate(ctx, req)
	case "watches.delete":
		return p.handleWatchesDelete(ctx, req)
	default:
		return nil, fmt.Errorf("redmine: unknown action %q", req.ActionKey)
	}
}

func jsonResponse(v any) (*pluginsdk.PluginActionResponse, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("redmine: encoding action response: %w", err)
	}
	return &pluginsdk.PluginActionResponse{Body: body}, nil
}

func decodeBody[T any](req *pluginsdk.PluginActionRequest) (T, error) {
	var out T
	if len(req.Body) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(req.Body, &out); err != nil {
		return out, fmt.Errorf("redmine: decoding action %q body: %w", req.ActionKey, err)
	}
	return out, nil
}

// errorResponse reports a distinct, plugin-owned action error rather than a
// bare host-level failure — the spec's requirement that a Redmine
// credential rejection never surfaces as a generic 401 to the frontend (see
// spec "Failure modes").
type actionErrorResponse struct {
	Error string `json:"error"`
	Kind  string `json:"kind"`
}

func classifiedErrorResponse(err error) (*pluginsdk.PluginActionResponse, error) {
	kind := "unexpected"
	if apiErr, ok := asAPIError(err); ok {
		kind = string(apiErr.Kind)
	}
	body, marshalErr := json.Marshal(actionErrorResponse{Error: err.Error(), Kind: kind})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &pluginsdk.PluginActionResponse{Body: body, Status: 200}, nil
}

// --- connection.* -----------------------------------------------------

type connectionResponse struct {
	State     string `json:"state"`
	BaseURL   string `json:"base_url,omitempty"`
	LastOK    string `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func (p *redminePlugin) handleConnectionGet(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	record, found, err := p.connectionSvc.Get(ctx, req.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return jsonResponse(connectionResponse{State: "disconnected"})
	}
	return jsonResponse(connectionResponse{
		State: string(record.State), BaseURL: record.BaseURL, LastOK: record.LastOK, LastError: record.LastError,
	})
}

type connectionSaveRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func (p *redminePlugin) handleConnectionSave(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[connectionSaveRequest](req)
	if err != nil {
		return nil, err
	}
	var record *connection.Record
	if body.APIKey == "" {
		record, err = p.connectionSvc.ConnectWithExistingKey(ctx, req.Context.WorkspaceID, body.BaseURL)
	} else {
		record, err = p.connectionSvc.Connect(ctx, req.Context.WorkspaceID, body.BaseURL, body.APIKey)
	}
	if err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(connectionResponse{State: string(record.State), BaseURL: record.BaseURL, LastOK: record.LastOK})
}

// handleConnectionDisconnect deletes every watch for the workspace first
// (cascading their created task trees via PluginOwnedTaskTrees) before
// removing the connection itself, so deleting a connection never leaves
// orphaned watcher tasks behind (spec "Failure modes").
func (p *redminePlugin) handleConnectionDisconnect(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if err := p.clearWorkspace(ctx, req.Context.WorkspaceID); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"disconnected": true})
}

// --- projects.* ---------------------------------------------------------

type projectsListResponse struct {
	Projects    []redmineProject `json:"projects"`
	SelectedIDs []int            `json:"selected_ids"`
}

type redmineProject struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

func (p *redminePlugin) handleProjectsList(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	live, err := p.projectsSvc.ListLive(ctx, client)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	selected, err := p.projectsSvc.GetSelection(ctx, req.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]redmineProject, len(live))
	for i, proj := range live {
		out[i] = redmineProject{ID: proj.ID, Name: proj.Name, Identifier: proj.Identifier}
	}
	return jsonResponse(projectsListResponse{Projects: out, SelectedIDs: selected})
}

type projectsSaveRequest struct {
	ProjectIDs []int `json:"project_ids"`
}

func (p *redminePlugin) handleProjectsSave(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[projectsSaveRequest](req)
	if err != nil {
		return nil, err
	}
	if err := p.projectsSvc.SaveSelection(ctx, req.Context.WorkspaceID, body.ProjectIDs); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"saved": true})
}

// --- fieldmapping.* -------------------------------------------------------

type fieldMappingGetResponse struct {
	Statuses            []fieldmapping.StatusMapping   `json:"statuses"`
	Trackers            []fieldmapping.TrackerMapping  `json:"trackers"`
	Priorities          []fieldmapping.PriorityMapping `json:"priorities"`
	LiveStatuses        []redmineNamedRef              `json:"live_statuses"`
	LiveTrackers        []redmineNamedRef              `json:"live_trackers"`
	LivePriorities      []redmineNamedRef              `json:"live_priorities"`
	CustomFields        []fieldmapping.CustomField     `json:"custom_fields"`
	CustomFieldsDerived bool                           `json:"custom_fields_derived"`
}

type redmineNamedRef struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IsClosed bool   `json:"is_closed,omitempty"`
}

func (p *redminePlugin) handleFieldMappingGet(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	liveStatuses, err := client.ListIssueStatuses(ctx)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	liveTrackers, err := client.ListTrackers(ctx)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	livePriorities, err := client.ListIssuePriorities(ctx)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	customFields, derived, err := p.resolveCustomFields(ctx, client)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	mapping, _, err := p.fieldmappingSvc.Get(ctx, req.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}

	return jsonResponse(fieldMappingGetResponse{
		Statuses: mapping.Statuses, Trackers: mapping.Trackers, Priorities: mapping.Priorities,
		LiveStatuses:        toNamedRefs(liveStatuses),
		LiveTrackers:        toTrackerRefs(liveTrackers),
		LivePriorities:      toPriorityRefs(livePriorities),
		CustomFields:        customFields,
		CustomFieldsDerived: derived,
	})
}

// resolveCustomFields fetches /custom_fields.json (admin key); on a
// non-admin 403 it derives fields from the union of custom fields observed
// on a recent page of issues instead of treating the 403 as an error (spec
// "Field mapping").
func (p *redminePlugin) resolveCustomFields(ctx context.Context, client *redmineclient.Client) ([]fieldmapping.CustomField, bool, error) {
	live, err := client.ListCustomFields(ctx)
	if err == nil {
		out := make([]fieldmapping.CustomField, len(live))
		for i, f := range live {
			out[i] = fieldmapping.CustomField{ID: f.ID, Name: f.Name}
		}
		return out, false, nil
	}
	apiErr, ok := asAPIError(err)
	if !ok || apiErr.Kind != redmineclient.ErrKindAPIDisabled {
		return nil, false, err
	}

	issuesSvc := issues.New(client)
	result, err := issuesSvc.ListIssues(ctx, issues.ListIssuesParams{Limit: 25})
	if err != nil {
		return nil, false, err
	}
	perIssue := make([][]fieldmapping.CustomField, len(result.Issues))
	for i, issue := range result.Issues {
		fields := make([]fieldmapping.CustomField, len(issue.CustomFields))
		for j, f := range issue.CustomFields {
			fields[j] = fieldmapping.CustomField{ID: f.ID, Name: f.Name}
		}
		perIssue[i] = fields
	}
	return fieldmapping.DeriveCustomFieldsFromIssues(perIssue), true, nil
}

func (p *redminePlugin) handleFieldMappingSave(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[fieldmapping.Mapping](req)
	if err != nil {
		return nil, err
	}
	if err := p.fieldmappingSvc.Save(ctx, req.Context.WorkspaceID, body); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"saved": true})
}

// --- syncoptions.* --------------------------------------------------------

func (p *redminePlugin) handleSyncOptionsSave(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[syncOptionsSaveRequest](req)
	if err != nil {
		return nil, err
	}
	if err := p.syncSvc.SaveOptions(ctx, req.Context.WorkspaceID, body.toOptions()); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"saved": true})
}

type syncOptionsSaveRequest struct {
	AutoStatusWriteback  bool `json:"auto_status_writeback"`
	SyncTitleDescription bool `json:"sync_title_description"`
}

func (r syncOptionsSaveRequest) toOptions() redminesync.Options {
	return redminesync.Options{AutoStatusWriteback: r.AutoStatusWriteback, SyncTitleDescription: r.SyncTitleDescription}
}

func toNamedRefs(statuses []redmineclient.IssueStatus) []redmineNamedRef {
	out := make([]redmineNamedRef, len(statuses))
	for i, s := range statuses {
		out[i] = redmineNamedRef{ID: s.ID, Name: s.Name, IsClosed: s.IsClosed}
	}
	return out
}

func toTrackerRefs(trackers []redmineclient.Tracker) []redmineNamedRef {
	out := make([]redmineNamedRef, len(trackers))
	for i, t := range trackers {
		out[i] = redmineNamedRef{ID: t.ID, Name: t.Name}
	}
	return out
}

func toPriorityRefs(priorities []redmineclient.Priority) []redmineNamedRef {
	out := make([]redmineNamedRef, len(priorities))
	for i, pr := range priorities {
		out[i] = redmineNamedRef{ID: pr.ID, Name: pr.Name}
	}
	return out
}
