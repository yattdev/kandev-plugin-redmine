// Package fieldmapping persists per-workspace status/tracker/priority
// mapping from live Redmine field data to Kandev concepts, and derives
// custom-field definitions from observed issues when /custom_fields.json is
// unavailable to a non-admin API key. No status, tracker, or priority name
// is ever hardcoded here — every mapping entry is built from data the caller
// fetched live via internal/redmineclient.
package fieldmapping

import (
	"context"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// StatusMapping maps one live Redmine issue status to a Kandev workflow
// step, capturing IsClosed so a closed-status transition can be recognized
// without a second live lookup.
type StatusMapping struct {
	RedmineStatusID int    `json:"redmine_status_id"`
	RedmineName     string `json:"redmine_name"`
	IsClosed        bool   `json:"is_closed"`
	WorkflowStepID  string `json:"workflow_step_id"`
}

// TrackerMapping maps one live Redmine tracker to a Kandev task label.
type TrackerMapping struct {
	RedmineTrackerID int    `json:"redmine_tracker_id"`
	RedmineName      string `json:"redmine_name"`
	TaskLabel        string `json:"task_label"`
}

// PriorityMapping maps one live Redmine priority to a Kandev task priority
// (critical|high|medium|low).
type PriorityMapping struct {
	RedminePriorityID int    `json:"redmine_priority_id"`
	RedmineName       string `json:"redmine_name"`
	TaskPriority      string `json:"task_priority"`
}

// CustomField is one Redmine custom field definition, either fetched live
// from /custom_fields.json (admin key) or derived from issues observed on
// the wire (DeriveCustomFieldsFromIssues).
type CustomField struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Mapping is the full persisted per-workspace field mapping.
type Mapping struct {
	Statuses   []StatusMapping   `json:"statuses"`
	Trackers   []TrackerMapping  `json:"trackers"`
	Priorities []PriorityMapping `json:"priorities"`
}

// WorkflowStepForStatus resolves the inbound direction: a Redmine status ID
// to the Kandev workflow step it maps to.
func (m Mapping) WorkflowStepForStatus(redmineStatusID int) (string, bool) {
	for _, s := range m.Statuses {
		if s.RedmineStatusID == redmineStatusID {
			return s.WorkflowStepID, s.WorkflowStepID != ""
		}
	}
	return "", false
}

// StatusForWorkflowStep resolves the outbound (write-back) direction: a
// Kandev workflow step to the Redmine status ID it maps to.
func (m Mapping) StatusForWorkflowStep(workflowStepID string) (int, bool) {
	for _, s := range m.Statuses {
		if s.WorkflowStepID == workflowStepID {
			return s.RedmineStatusID, true
		}
	}
	return 0, false
}

const (
	stateScope = "workspace"
	stateKey   = "field_mapping"
)

type Service struct {
	host pluginsdk.Host
}

func New(host pluginsdk.Host) *Service {
	return &Service{host: host}
}

func (s *Service) Save(ctx context.Context, workspaceID string, mapping Mapping) error {
	if err := s.host.SetState(ctx, stateScope, workspaceID, stateKey, mapping.toMap()); err != nil {
		return fmt.Errorf("fieldmapping: saving: %w", err)
	}
	return nil
}

func (s *Service) Get(ctx context.Context, workspaceID string) (Mapping, bool, error) {
	value, found, err := s.host.GetState(ctx, stateScope, workspaceID, stateKey)
	if err != nil {
		return Mapping{}, false, fmt.Errorf("fieldmapping: reading: %w", err)
	}
	if !found {
		return Mapping{}, false, nil
	}
	return mappingFromMap(value), true, nil
}

// DeriveCustomFieldsFromIssues unions the custom fields observed across a
// set of already-fetched issues, deduplicated by field ID — the fallback the
// spec requires when /custom_fields.json 403s for a non-admin API key,
// surfaced to the UI layer with a "derived from recent issues" note (task
// 08) rather than treated as an error.
func DeriveCustomFieldsFromIssues(issuesFields [][]CustomField) []CustomField {
	seen := make(map[int]CustomField)
	order := make([]int, 0)
	for _, fields := range issuesFields {
		for _, f := range fields {
			if _, ok := seen[f.ID]; !ok {
				order = append(order, f.ID)
			}
			seen[f.ID] = f
		}
	}
	out := make([]CustomField, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func (m Mapping) toMap() map[string]any {
	statuses := make([]any, len(m.Statuses))
	for i, s := range m.Statuses {
		statuses[i] = map[string]any{
			"redmine_status_id": s.RedmineStatusID,
			"redmine_name":      s.RedmineName,
			"is_closed":         s.IsClosed,
			"workflow_step_id":  s.WorkflowStepID,
		}
	}
	trackers := make([]any, len(m.Trackers))
	for i, tr := range m.Trackers {
		trackers[i] = map[string]any{
			"redmine_tracker_id": tr.RedmineTrackerID,
			"redmine_name":       tr.RedmineName,
			"task_label":         tr.TaskLabel,
		}
	}
	priorities := make([]any, len(m.Priorities))
	for i, p := range m.Priorities {
		priorities[i] = map[string]any{
			"redmine_priority_id": p.RedminePriorityID,
			"redmine_name":        p.RedmineName,
			"task_priority":       p.TaskPriority,
		}
	}
	return map[string]any{
		"statuses":   statuses,
		"trackers":   trackers,
		"priorities": priorities,
	}
}

func mappingFromMap(m map[string]any) Mapping {
	out := Mapping{}
	for _, raw := range asSlice(m["statuses"]) {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out.Statuses = append(out.Statuses, StatusMapping{
			RedmineStatusID: asInt(row["redmine_status_id"]),
			RedmineName:     asString(row["redmine_name"]),
			IsClosed:        asBool(row["is_closed"]),
			WorkflowStepID:  asString(row["workflow_step_id"]),
		})
	}
	for _, raw := range asSlice(m["trackers"]) {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out.Trackers = append(out.Trackers, TrackerMapping{
			RedmineTrackerID: asInt(row["redmine_tracker_id"]),
			RedmineName:      asString(row["redmine_name"]),
			TaskLabel:        asString(row["task_label"]),
		})
	}
	for _, raw := range asSlice(m["priorities"]) {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out.Priorities = append(out.Priorities, PriorityMapping{
			RedminePriorityID: asInt(row["redmine_priority_id"]),
			RedmineName:       asString(row["redmine_name"]),
			TaskPriority:      asString(row["task_priority"]),
		})
	}
	return out
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}
