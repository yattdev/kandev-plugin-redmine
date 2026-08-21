// Package sync owns the cursor-based inbound polling loop and the opt-in
// outbound write-back, wired to internal/tasklink for echo suppression.
//
// Write-back has two entry points that share PushWriteback: the OnEvent
// "task.moved" handler (server/plugin.go) for near-real-time delivery, and
// this package's own reconciliation pass over linked tasks (called from the
// same ticker as PollInbound) as a backstop — OnEvent delivery is
// best-effort (bounded in-memory queue, lost on backend restart or sustained
// overload per the plugin host's delivery contract), so a missed event still
// converges on the next poll instead of leaving the write-back stuck.
package sync

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"kandev-plugin-redmine/internal/fieldmapping"
	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/tasklink"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// cursorOverlap subtracts one second from the persisted cursor before using
// it as the next poll's lower bound — Redmine's updated_on has only
// second-granularity, so an issue updated in the same second as the
// previous poll's newest-seen timestamp could otherwise be skipped.
const cursorOverlap = 1 * time.Second

const (
	stateScope = "workspace"
	cursorKey  = "sync_cursor"
	optionsKey = "sync_options"
)

// Options are the two per-connection opt-in sync toggles from the spec.
type Options struct {
	AutoStatusWriteback  bool
	SyncTitleDescription bool
}

// SaveOptions persists the two sync toggles for workspaceID.
func (s *Service) SaveOptions(ctx context.Context, workspaceID string, opts Options) error {
	if err := s.host.SetState(ctx, stateScope, workspaceID, optionsKey, map[string]any{
		"auto_status_writeback":  opts.AutoStatusWriteback,
		"sync_title_description": opts.SyncTitleDescription,
	}); err != nil {
		return fmt.Errorf("sync: saving options: %w", err)
	}
	return nil
}

// GetOptions returns the persisted sync toggles for workspaceID (both false
// if never saved — manual write-back only by default, per spec).
func (s *Service) GetOptions(ctx context.Context, workspaceID string) (Options, error) {
	value, found, err := s.host.GetState(ctx, stateScope, workspaceID, optionsKey)
	if err != nil {
		return Options{}, fmt.Errorf("sync: reading options: %w", err)
	}
	if !found {
		return Options{}, nil
	}
	autoWriteback, _ := value["auto_status_writeback"].(bool)
	syncTitleDesc, _ := value["sync_title_description"].(bool)
	return Options{AutoStatusWriteback: autoWriteback, SyncTitleDescription: syncTitleDesc}, nil
}

func (s *Service) Clear(ctx context.Context, workspaceID string) error {
	if err := s.host.DeleteState(ctx, stateScope, workspaceID, cursorKey); err != nil {
		return fmt.Errorf("sync: clearing cursor: %w", err)
	}
	if err := s.host.DeleteState(ctx, stateScope, workspaceID, optionsKey); err != nil {
		return fmt.Errorf("sync: clearing options: %w", err)
	}
	return nil
}

type Service struct {
	host     pluginsdk.Host
	tasklink *tasklink.Service
}

func New(host pluginsdk.Host, tasklinkSvc *tasklink.Service) *Service {
	return &Service{host: host, tasklink: tasklinkSvc}
}

// PollInbound fetches issues updated since the persisted cursor (1s overlap)
// across projectIDs, applies mapped changes to already-linked tasks
// (skipping self-authored echoes), and advances the cursor to the newest
// observed updated_on. A workspace with no selected projects is a no-op.
func (s *Service) PollInbound(ctx context.Context, workspaceID string, issuesSvc *issues.Service, mapping fieldmapping.Mapping, projectIDs []int, opts Options) error {
	if len(projectIDs) == 0 {
		return nil
	}

	cursor, err := s.cursor(ctx, workspaceID)
	if err != nil {
		return err
	}
	var since string
	if !cursor.IsZero() {
		since = ">=" + cursor.Add(-cursorOverlap).UTC().Format(time.RFC3339)
	}

	var newest time.Time
	for _, projectID := range projectIDs {
		latest, err := s.pollProject(ctx, workspaceID, issuesSvc, mapping, strconv.Itoa(projectID), since, opts)
		if err != nil {
			return err
		}
		if latest.After(newest) {
			newest = latest
		}
	}

	if !newest.IsZero() {
		if err := s.saveCursor(ctx, workspaceID, newest); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) pollProject(ctx context.Context, workspaceID string, issuesSvc *issues.Service, mapping fieldmapping.Mapping, projectID, since string, opts Options) (time.Time, error) {
	var newest time.Time
	offset := 0
	for {
		result, err := issuesSvc.ListIssues(ctx, issues.ListIssuesParams{
			ProjectID: projectID, UpdatedOnFrom: since, Offset: offset, Limit: 100,
		})
		if err != nil {
			return newest, err
		}
		for _, issue := range result.Issues {
			if err := s.applyInbound(ctx, workspaceID, issue, mapping, opts); err != nil {
				return newest, err
			}
			if t, err := time.Parse(time.RFC3339, issue.UpdatedOn); err == nil && t.After(newest) {
				newest = t
			}
		}
		offset += len(result.Issues)
		if len(result.Issues) == 0 || offset >= result.TotalCount {
			return newest, nil
		}
	}
}

// applyInbound updates the task linked to issue (if any), skipping any field
// whose incoming value matches what this plugin itself last pushed
// (echo suppression).
func (s *Service) applyInbound(ctx context.Context, workspaceID string, issue issues.Issue, mapping fieldmapping.Mapping, opts Options) error {
	taskID, found, err := s.tasklink.TaskIDForIssue(ctx, workspaceID, issue.ID)
	if err != nil || !found {
		return err
	}
	link, found, err := s.tasklink.Get(ctx, taskID)
	if err != nil || !found {
		return err
	}

	update := pluginsdk.UpdateTaskInput{ID: taskID}
	changed := false
	statusEcho, titleEcho, descriptionEcho := false, false, false

	if stepID, ok := mapping.WorkflowStepForStatus(issue.StatusID); ok {
		statusEcho = link.LastPushedStatusID != nil && *link.LastPushedStatusID == issue.StatusID
		if !statusEcho {
			update.WorkflowStepID = &stepID
			changed = true
		}
	}

	if opts.SyncTitleDescription {
		titleEcho = link.LastPushedTitle != "" && link.LastPushedTitle == issue.Subject
		descriptionEcho = link.LastPushedDescriptionHash != "" && link.LastPushedDescriptionHash == tasklink.HashDescription(issue.Description)
		if applyTitleAndDescription(&update, issue, *link, titleEcho, descriptionEcho) {
			changed = true
		}
	}
	if !changed {
		if statusEcho || titleEcho || descriptionEcho {
			return s.tasklink.ConsumeEcho(ctx, taskID, statusEcho, titleEcho, descriptionEcho)
		}
		return nil
	}
	if _, err := s.host.Tasks().Update(ctx, update); err != nil {
		return fmt.Errorf("sync: applying inbound update to task %s: %w", taskID, err)
	}
	if statusEcho || titleEcho || descriptionEcho {
		if err := s.tasklink.ConsumeEcho(ctx, taskID, statusEcho, titleEcho, descriptionEcho); err != nil {
			return err
		}
	}
	return nil
}

func applyTitleAndDescription(update *pluginsdk.UpdateTaskInput, issue issues.Issue, link tasklink.Link, titleEcho, descriptionEcho bool) bool {
	changed := false
	if !titleEcho && issue.Subject != "" {
		subject := issue.Subject
		update.Title = &subject
		changed = true
	}
	if !descriptionEcho {
		description := issue.Description
		update.Description = &description
		changed = true
	}
	return changed
}

// PushWriteback issues an outbound status PUT when a linked task moves to a
// workflow step with a status mapping, provided opts.AutoStatusWriteback is
// enabled. A no-op when the task isn't linked, the step has no mapping, the
// option is disabled, or the mapped status already matches what was last
// pushed (idempotent against a duplicate task.moved delivery or a
// reconciliation poll re-observing the same, already-applied state).
func (s *Service) PushWriteback(ctx context.Context, taskID, toWorkflowStepID string, mapping fieldmapping.Mapping, issuesSvc *issues.Service, opts Options) error {
	if !opts.AutoStatusWriteback {
		return nil
	}
	statusID, ok := mapping.StatusForWorkflowStep(toWorkflowStepID)
	if !ok {
		return nil
	}
	return s.forceWriteback(ctx, taskID, statusID, issuesSvc)
}

// ForceWriteback is the manual "Set Redmine status" action's entry point
// (task 08's UI, wired through the link.set_status manifest action) — it
// pushes unconditionally, ignoring AutoStatusWriteback, since the operator
// explicitly asked for it.
func (s *Service) ForceWriteback(ctx context.Context, taskID string, statusID int, issuesSvc *issues.Service) error {
	return s.forceWriteback(ctx, taskID, statusID, issuesSvc)
}

func (s *Service) forceWriteback(ctx context.Context, taskID string, statusID int, issuesSvc *issues.Service) error {
	link, found, err := s.tasklink.Get(ctx, taskID)
	if err != nil || !found {
		return err
	}
	if link.LastPushedStatusID != nil && *link.LastPushedStatusID == statusID {
		return nil
	}
	// Echo markers are intentionally one-shot. Once inbound polling consumes
	// one, a redelivered task.moved must still not issue a second PUT when
	// Redmine already reflects the requested status.
	issue, err := issuesSvc.GetIssue(ctx, link.IssueID)
	if err != nil {
		return fmt.Errorf("sync: reading issue %d before write-back: %w", link.IssueID, err)
	}
	if issue.StatusID == statusID {
		return nil
	}
	if err := issuesSvc.UpdateIssue(ctx, link.IssueID, issues.IssueWrite{StatusID: statusID}); err != nil {
		return fmt.Errorf("sync: writing back status for task %s: %w", taskID, err)
	}
	return s.tasklink.RecordPushedStatus(ctx, taskID, statusID)
}

func (s *Service) cursor(ctx context.Context, workspaceID string) (time.Time, error) {
	value, found, err := s.host.GetState(ctx, stateScope, workspaceID, cursorKey)
	if err != nil {
		return time.Time{}, fmt.Errorf("sync: reading cursor: %w", err)
	}
	if !found {
		return time.Time{}, nil
	}
	raw, ok := value["updated_on"].(string)
	if !ok {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, nil
	}
	return t, nil
}

func (s *Service) saveCursor(ctx context.Context, workspaceID string, t time.Time) error {
	if err := s.host.SetState(ctx, stateScope, workspaceID, cursorKey, map[string]any{"updated_on": t.UTC().Format(time.RFC3339)}); err != nil {
		return fmt.Errorf("sync: saving cursor: %w", err)
	}
	return nil
}
