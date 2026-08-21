package main

import (
	"context"
	"fmt"

	"kandev-plugin-redmine/internal/watch"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type watchResponse struct {
	ID               string `json:"id"`
	WorkflowID       string `json:"workflow_id"`
	WorkflowStepID   string `json:"workflow_step_id"`
	ProjectID        int    `json:"project_id"`
	TrackerID        *int   `json:"tracker_id,omitempty"`
	StatusID         *int   `json:"status_id,omitempty"`
	MaxInflightTasks int    `json:"max_inflight_tasks"`
	Enabled          bool   `json:"enabled"`
}

func toWatchResponse(w watch.Watch) watchResponse {
	return watchResponse{
		ID: w.ID, WorkflowID: w.WorkflowID, WorkflowStepID: w.WorkflowStepID, ProjectID: w.ProjectID, TrackerID: w.TrackerID, StatusID: w.StatusID,
		MaxInflightTasks: w.MaxInflightTasks, Enabled: w.Enabled,
	}
}

func (p *redminePlugin) handleWatchesList(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	watches, err := p.watchSvc.ListWatches(ctx, req.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]watchResponse, len(watches))
	for i, w := range watches {
		out[i] = toWatchResponse(w)
	}
	return jsonResponse(map[string]any{"watches": out})
}

type watchSaveRequest struct {
	ID               string `json:"id"`
	ProjectID        int    `json:"project_id"`
	TrackerID        *int   `json:"tracker_id"`
	StatusID         *int   `json:"status_id"`
	MaxInflightTasks int    `json:"max_inflight_tasks"`
	Enabled          bool   `json:"enabled"`
}

func (r watchSaveRequest) toWatch(workspaceID string) watch.Watch {
	return watch.Watch{
		ID: r.ID, WorkspaceID: workspaceID, ProjectID: r.ProjectID, TrackerID: r.TrackerID,
		StatusID: r.StatusID, MaxInflightTasks: r.MaxInflightTasks, Enabled: r.Enabled,
	}
}

func (p *redminePlugin) handleWatchesCreate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[watchSaveRequest](req)
	if err != nil {
		return nil, err
	}
	w, err := p.watchWithPlacement(ctx, req.Context.WorkspaceID, body.toWatch(req.Context.WorkspaceID))
	if err != nil {
		return nil, err
	}
	created, err := p.watchSvc.CreateWatch(ctx, w)
	if err != nil {
		return nil, err
	}
	return jsonResponse(toWatchResponse(created))
}

func (p *redminePlugin) handleWatchesUpdate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[watchSaveRequest](req)
	if err != nil {
		return nil, err
	}
	w, err := p.watchWithPlacement(ctx, req.Context.WorkspaceID, body.toWatch(req.Context.WorkspaceID))
	if err != nil {
		return nil, err
	}
	if err := p.watchSvc.UpdateWatch(ctx, w); err != nil {
		return nil, err
	}
	return jsonResponse(toWatchResponse(w))
}

func (p *redminePlugin) watchWithPlacement(ctx context.Context, workspaceID string, w watch.Watch) (watch.Watch, error) {
	mapping, found, err := p.fieldmappingSvc.Get(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	if !found || mapping.WorkflowID == "" {
		return w, fmt.Errorf("redmine: save a workflow mapping before creating a watch")
	}
	w.WorkflowID = mapping.WorkflowID
	if w.StatusID != nil {
		w.WorkflowStepID, _ = mapping.WorkflowStepForStatus(*w.StatusID)
	}
	w.TrackerLabels = make(map[int]string, len(mapping.Trackers))
	for _, tracker := range mapping.Trackers {
		w.TrackerLabels[tracker.RedmineTrackerID] = tracker.TaskLabel
	}
	w.PriorityMappings = make(map[int]string, len(mapping.Priorities))
	for _, priority := range mapping.Priorities {
		w.PriorityMappings[priority.RedminePriorityID] = priority.TaskPriority
	}
	return w, nil
}

type watchDeleteRequest struct {
	ID string `json:"id"`
}

func (p *redminePlugin) handleWatchesDelete(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[watchDeleteRequest](req)
	if err != nil {
		return nil, err
	}
	if err := p.watchSvc.DeleteWatch(ctx, req.Context.WorkspaceID, body.ID); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"deleted": true})
}
