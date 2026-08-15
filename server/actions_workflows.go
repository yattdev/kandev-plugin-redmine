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
	workflows, _, err := host.Workflows().List(ctx, req.Context.WorkspaceID, pluginsdk.Page{})
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
