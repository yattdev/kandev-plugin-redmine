package main

import (
	"context"
	"fmt"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"
	"kandev-plugin-redmine/internal/watch"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

func (p *redminePlugin) handleWatchesPoll(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	if err := p.pollWatches(ctx, req.Context.WorkspaceID, issues.New(client)); err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]any{"polled": true})
}

type watchResponse struct {
	ID                 string         `json:"id"`
	WorkflowID         string         `json:"workflow_id"`
	WorkflowStepID     string         `json:"workflow_step_id"`
	ProjectID          int            `json:"project_id"`
	TrackerID          *int           `json:"tracker_id,omitempty"`
	StatusID           *int           `json:"status_id,omitempty"`
	PriorityID         *int           `json:"priority_id,omitempty"`
	AssigneeID         *int           `json:"assignee_id,omitempty"`
	CategoryID         *int           `json:"category_id,omitempty"`
	CustomFieldFilters map[int]string `json:"custom_field_filters,omitempty"`
	MaxInflightTasks   int            `json:"max_inflight_tasks"`
	Enabled            bool           `json:"enabled"`
}

func toWatchResponse(w watch.Watch) watchResponse {
	return watchResponse{
		ID: w.ID, WorkflowID: w.WorkflowID, WorkflowStepID: w.WorkflowStepID, ProjectID: w.ProjectID, TrackerID: w.TrackerID, StatusID: w.StatusID, PriorityID: w.PriorityID, AssigneeID: w.AssigneeID, CategoryID: w.CategoryID, CustomFieldFilters: w.CustomFieldFilters,
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
	ID                 string         `json:"id"`
	ProjectID          int            `json:"project_id"`
	TrackerID          *int           `json:"tracker_id"`
	StatusID           *int           `json:"status_id"`
	PriorityID         *int           `json:"priority_id"`
	AssigneeID         *int           `json:"assignee_id"`
	CategoryID         *int           `json:"category_id"`
	CustomFieldFilters map[int]string `json:"custom_field_filters"`
	MaxInflightTasks   int            `json:"max_inflight_tasks"`
	Enabled            bool           `json:"enabled"`
}

func (r watchSaveRequest) toWatch(workspaceID string) watch.Watch {
	return watch.Watch{
		ID: r.ID, WorkspaceID: workspaceID, ProjectID: r.ProjectID, TrackerID: r.TrackerID,
		StatusID: r.StatusID, PriorityID: r.PriorityID, AssigneeID: r.AssigneeID, CategoryID: r.CategoryID, CustomFieldFilters: r.CustomFieldFilters, MaxInflightTasks: r.MaxInflightTasks, Enabled: r.Enabled,
	}
}

func (p *redminePlugin) handleWatchesCreate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[watchSaveRequest](req)
	if err != nil {
		return nil, err
	}
	w := body.toWatch(req.Context.WorkspaceID)
	if err := p.validateWatch(ctx, w); err != nil {
		return classifiedErrorResponse(err)
	}
	w, err = p.watchWithPlacement(ctx, req.Context.WorkspaceID, w)
	if err != nil {
		return classifiedErrorResponse(err)
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
	w := body.toWatch(req.Context.WorkspaceID)
	if err := p.validateWatch(ctx, w); err != nil {
		return classifiedErrorResponse(err)
	}
	w, err = p.watchWithPlacement(ctx, req.Context.WorkspaceID, w)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	if err := p.watchSvc.UpdateWatch(ctx, w); err != nil {
		return nil, err
	}
	return jsonResponse(toWatchResponse(w))
}

func (p *redminePlugin) validateWatch(ctx context.Context, w watch.Watch) error {
	if w.ProjectID <= 0 {
		return fmt.Errorf("redmine: project_id must be positive")
	}
	if w.MaxInflightTasks < 0 {
		return fmt.Errorf("redmine: max_inflight_tasks must be non-negative")
	}
	for name, id := range map[string]*int{"tracker_id": w.TrackerID, "status_id": w.StatusID, "priority_id": w.PriorityID, "assignee_id": w.AssigneeID, "category_id": w.CategoryID} {
		if id != nil && *id <= 0 {
			return fmt.Errorf("redmine: %s must be positive", name)
		}
	}
	for id, value := range w.CustomFieldFilters {
		if id <= 0 || value == "" {
			return fmt.Errorf("redmine: custom field filters require a positive ID and value")
		}
	}
	selected, err := p.projectsSvc.GetSelection(ctx, w.WorkspaceID)
	if err != nil {
		return err
	}
	for _, projectID := range selected {
		if projectID == w.ProjectID {
			client, err := p.connectionSvc.Client(ctx, w.WorkspaceID)
			if err != nil {
				return err
			}
			if w.TrackerID != nil {
				values, err := client.ListTrackers(ctx)
				if err != nil {
					return err
				}
				found := false
				for _, value := range values {
					if value.ID == *w.TrackerID {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("redmine: tracker_id %d is not available", *w.TrackerID)
				}
			}
			if w.StatusID != nil {
				values, err := client.ListIssueStatuses(ctx)
				if err != nil {
					return err
				}
				found := false
				for _, value := range values {
					if value.ID == *w.StatusID {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("redmine: status_id %d is not available", *w.StatusID)
				}
			}
			if w.PriorityID != nil {
				values, err := client.ListIssuePriorities(ctx)
				if err != nil {
					return err
				}
				found := false
				for _, value := range values {
					if value.ID == *w.PriorityID {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("redmine: priority_id %d is not available", *w.PriorityID)
				}
			}
			if w.AssigneeID != nil {
				values, err := client.ListProjectMembers(ctx, w.ProjectID)
				if err != nil {
					return err
				}
				if !containsOption(values, w.AssigneeID) {
					return fmt.Errorf("redmine: assignee_id %d is not available", *w.AssigneeID)
				}
			}
			if w.CategoryID != nil {
				values, err := client.ListIssueCategories(ctx, w.ProjectID)
				if err != nil {
					return err
				}
				if !containsOption(values, w.CategoryID) {
					return fmt.Errorf("redmine: category_id %d is not available", *w.CategoryID)
				}
			}
			if len(w.CustomFieldFilters) > 0 {
				options, err := p.loadWatchFilterOptions(ctx, client, w.ProjectID)
				if err != nil {
					return err
				}
				for id, value := range w.CustomFieldFilters {
					if values, ok := options.CustomFieldValues[id]; !ok || !containsString(values, value) {
						return fmt.Errorf("redmine: custom field filter %d is not available", id)
					}
				}
			}
			return nil
		}
	}
	return fmt.Errorf("redmine: project_id %d is not selected for this workspace", w.ProjectID)
}

type watchFilterOptionsRequest struct {
	ProjectID int `json:"project_id"`
}
type watchFilterOptionsResponse struct {
	Trackers          []redmineclient.NamedID  `json:"trackers"`
	Statuses          []redmineclient.NamedID  `json:"statuses"`
	Priorities        []redmineclient.NamedID  `json:"priorities"`
	Assignees         []redmineclient.NamedID  `json:"assignees"`
	Categories        []redmineclient.NamedID  `json:"categories"`
	CustomFieldValues map[int][]string         `json:"custom_field_values"`
	CustomFields      []watchCustomFieldOption `json:"custom_fields"`
}

type watchCustomFieldOption struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

func (p *redminePlugin) handleWatchFilterOptions(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[watchFilterOptionsRequest](req)
	if err != nil {
		return nil, err
	}
	if body.ProjectID <= 0 {
		return classifiedErrorResponse(fmt.Errorf("redmine: project_id must be positive"))
	}
	selected, err := p.projectsSvc.GetSelection(ctx, req.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, id := range selected {
		if id == body.ProjectID {
			found = true
			break
		}
	}
	if !found {
		return classifiedErrorResponse(fmt.Errorf("redmine: project_id %d is not selected for this workspace", body.ProjectID))
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	options, err := p.loadWatchFilterOptions(ctx, client, body.ProjectID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(options)
}

func (p *redminePlugin) loadWatchFilterOptions(ctx context.Context, client *redmineclient.Client, projectID int) (watchFilterOptionsResponse, error) {
	trackers, err := client.ListTrackers(ctx)
	if err != nil {
		return watchFilterOptionsResponse{}, err
	}
	statuses, err := client.ListIssueStatuses(ctx)
	if err != nil {
		return watchFilterOptionsResponse{}, err
	}
	priorities, err := client.ListIssuePriorities(ctx)
	if err != nil {
		return watchFilterOptionsResponse{}, err
	}
	members, err := client.ListProjectMembers(ctx, projectID)
	if err != nil {
		return watchFilterOptionsResponse{}, err
	}
	categories, err := client.ListIssueCategories(ctx, projectID)
	if err != nil {
		return watchFilterOptionsResponse{}, err
	}
	out := watchFilterOptionsResponse{Trackers: namedTrackers(trackers), Statuses: namedStatuses(statuses), Priorities: namedPriorities(priorities), Assignees: members, Categories: categories, CustomFieldValues: map[int][]string{}}
	fields, fieldErr := client.ListCustomFields(ctx)
	fieldNames := map[int]string{}
	deriveValues := fieldErr != nil
	if fieldErr == nil {
		for _, field := range fields {
			out.CustomFieldValues[field.ID] = append([]string(nil), field.PossibleValues...)
			fieldNames[field.ID] = field.Name
			if len(field.PossibleValues) == 0 {
				deriveValues = true
			}
		}
	}
	// A non-admin key cannot list field definitions, and text custom fields do
	// not declare possible_values. In both cases derive visible values from
	// this project's issues, mirroring the mapping fallback.
	if deriveValues {
		page, err := issues.New(client).ListIssues(ctx, issues.ListIssuesParams{ProjectID: fmt.Sprint(projectID), Limit: 100})
		if err != nil {
			return out, nil
		}
		for _, issue := range page.Issues {
			for _, field := range issue.CustomFields {
				fieldNames[field.ID] = field.Name
				if field.Value == nil {
					continue
				}
				value := fmt.Sprint(field.Value)
				if value != "" && value != "<nil>" && !containsString(out.CustomFieldValues[field.ID], value) {
					out.CustomFieldValues[field.ID] = append(out.CustomFieldValues[field.ID], value)
				}
			}
		}
	}
	for id, values := range out.CustomFieldValues {
		out.CustomFields = append(out.CustomFields, watchCustomFieldOption{ID: id, Name: fieldNames[id], Values: values})
	}
	return out, nil
}

func namedTrackers(in []redmineclient.Tracker) []redmineclient.NamedID {
	out := make([]redmineclient.NamedID, len(in))
	for i, v := range in {
		out[i] = redmineclient.NamedID{ID: v.ID, Name: v.Name}
	}
	return out
}
func namedStatuses(in []redmineclient.IssueStatus) []redmineclient.NamedID {
	out := make([]redmineclient.NamedID, len(in))
	for i, v := range in {
		out[i] = redmineclient.NamedID{ID: v.ID, Name: v.Name}
	}
	return out
}
func namedPriorities(in []redmineclient.Priority) []redmineclient.NamedID {
	out := make([]redmineclient.NamedID, len(in))
	for i, v := range in {
		out[i] = redmineclient.NamedID{ID: v.ID, Name: v.Name}
	}
	return out
}
func containsOption(options []redmineclient.NamedID, id *int) bool {
	if id == nil {
		return true
	}
	for _, option := range options {
		if option.ID == *id {
			return true
		}
	}
	return false
}
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (p *redminePlugin) watchWithPlacement(ctx context.Context, workspaceID string, w watch.Watch) (watch.Watch, error) {
	mapping, found, err := p.fieldmappingSvc.Get(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	if !found || mapping.WorkflowID == "" {
		return w, fmt.Errorf("redmine: save a workflow mapping before creating a watch")
	}
	return applyWatchMapping(w, mapping), nil
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
