package main

import (
	"context"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type workflowOption struct {
	ID    string               `json:"id"`
	Name  string               `json:"name"`
	Steps []workflowStepOption `json:"steps"`
}

type workflowStepOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleWorkflowsList lists the workspace's workflows with their steps, so
// the field-mapping UI can offer a real workflow-step picker rather than a
// free-text id (spec scopes one connection to exactly one Kandev workflow).
func (p *redminePlugin) handleWorkflowsList(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	host := p.Host()
	if host == nil {
		return nil, fmt.Errorf("redmine: host unavailable")
	}
	workflows, err := listWorkspaceWorkflows(ctx, host.Workflows(), req.Context.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("redmine: listing workflows: %w", err)
	}
	out := make([]workflowOption, len(workflows))
	for i, wf := range workflows {
		steps, err := host.Workflows().ListSteps(ctx, wf.ID)
		if err != nil {
			return nil, fmt.Errorf("redmine: listing steps for workflow %s: %w", wf.ID, err)
		}
		stepOptions := make([]workflowStepOption, len(steps))
		for j, step := range steps {
			stepOptions[j] = workflowStepOption{ID: step.ID, Name: step.Name}
		}
		out[i] = workflowOption{ID: wf.ID, Name: wf.Name, Steps: stepOptions}
	}
	return jsonResponse(map[string]any{"workflows": out})
}

// listWorkspaceWorkflows exhausts the Host data API's opaque cursor. A zero
// Page requests the Host's default page size; callers must still follow
// PageInfo because a workspace may contain more workflows than that default.
// Invalid or repeated cursors fail closed instead of returning a silently
// incomplete list or spinning forever on a malformed Host response.
func listWorkspaceWorkflows(ctx context.Context, reader pluginsdk.WorkflowReader, workspaceID string) ([]pluginsdk.Workflow, error) {
	var workflows []pluginsdk.Workflow
	page := pluginsdk.Page{}
	seenCursors := map[string]struct{}{page.Cursor: {}}
	for {
		items, pageInfo, err := reader.List(ctx, workspaceID, page)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, items...)
		if pageInfo == nil || !pageInfo.HasMore {
			return workflows, nil
		}
		nextCursor := pageInfo.NextCursor
		if nextCursor == "" {
			return nil, fmt.Errorf("workflow pagination returned an empty next cursor")
		}
		if _, seen := seenCursors[nextCursor]; seen {
			return nil, fmt.Errorf("workflow pagination repeated cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		page.Cursor = nextCursor
	}
}
