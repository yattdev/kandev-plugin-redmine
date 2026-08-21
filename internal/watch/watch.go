// Package watch implements structured-filter issue watches: one Kandev task
// per newly matching Redmine issue, deduplicated by (issue_watch_id,
// issue_id), with a per-watch maxInflightTasks throttle that actually
// enforces. Watcher-created tasks carry plugin:<id> provenance (stamped by
// the host on every Tasks().Create, per pluginsdk's Host.Tasks() doc) so
// PluginOwnedTaskTrees cleans them up when the watch or connection is
// removed.
package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"

	"kandev-plugin-redmine/internal/issues"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// metadataKeyWatchID is the single source of truth for the task-metadata key
// this package writes at creation (createTask) and reads back at
// throttle-check time (inflightCount). Task 07's highest risk (the exact bug
// Sentry's native integration originally shipped with) is these two sites
// disagreeing on the key name — sharing one constant makes that impossible
// by construction rather than by convention.
const metadataKeyWatchID = "redmine_watch_id"
const metadataKeyIssueID = "redmine_issue_id"

// terminalTaskStates mirrors pkg/api/v1.IsTerminalTaskState's value set
// (COMPLETED/FAILED/CANCELLED) — a watcher task in any other state still
// counts against maxInflightTasks.
var terminalTaskStates = map[string]bool{"COMPLETED": true, "FAILED": true, "CANCELLED": true}

// Watch is one structured issue-watch definition.
type Watch struct {
	ID               string
	WorkspaceID      string
	ProjectID        int
	TrackerID        *int
	StatusID         *int
	MaxInflightTasks int // 0 = unlimited
	Enabled          bool
}

func (w Watch) matches(issue issues.Issue) bool {
	if w.TrackerID != nil && *w.TrackerID != issue.TrackerID {
		return false
	}
	if w.StatusID != nil && *w.StatusID != issue.StatusID {
		return false
	}
	return true
}

const (
	workspaceScope = "workspace"
	watchesKey     = "watches"
	watchScope     = "watch"
	tasksKey       = "tasks"
)

type Service struct {
	host pluginsdk.Host
}

func New(host pluginsdk.Host) *Service {
	return &Service{host: host}
}

// CreateWatch persists a new watch with a fresh ID.
func (s *Service) CreateWatch(ctx context.Context, w Watch) (Watch, error) {
	id, err := newWatchID()
	if err != nil {
		return Watch{}, err
	}
	w.ID = id
	if err := s.saveWatchInList(ctx, w); err != nil {
		return Watch{}, err
	}
	return w, nil
}

// UpdateWatch persists changes to an existing watch (identified by w.ID),
// including enabling/disabling it.
func (s *Service) UpdateWatch(ctx context.Context, w Watch) error {
	if w.ID == "" {
		return fmt.Errorf("watch: id is required for update")
	}
	watches, err := s.ListWatches(ctx, w.WorkspaceID)
	if err != nil {
		return err
	}
	for _, existing := range watches {
		if existing.ID == w.ID {
			return s.saveWatchInList(ctx, w)
		}
	}
	return fmt.Errorf("watch: %s does not belong to workspace %s", w.ID, w.WorkspaceID)
}

// ListWatches returns every watch for workspaceID.
func (s *Service) ListWatches(ctx context.Context, workspaceID string) ([]Watch, error) {
	value, found, err := s.host.GetState(ctx, workspaceScope, workspaceID, watchesKey)
	if err != nil {
		return nil, fmt.Errorf("watch: reading watches: %w", err)
	}
	if !found {
		return nil, nil
	}
	raw, _ := value["watches"].([]any)
	out := make([]Watch, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, watchFromMap(workspaceID, row))
	}
	return out, nil
}

// DeleteWatch removes the watch and cascade-deletes every task it created
// via PluginOwnedTaskTrees, so disabling/removing a watch never leaves
// orphaned tasks behind.
func (s *Service) DeleteWatch(ctx context.Context, workspaceID, watchID string) error {
	if watchID == "" {
		return fmt.Errorf("watch: id is required for delete")
	}
	watches, err := s.ListWatches(ctx, workspaceID)
	if err != nil {
		return err
	}
	found := false
	for _, w := range watches {
		if w.ID == watchID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("watch: %s does not belong to workspace %s", watchID, workspaceID)
	}
	tasks, err := s.watchTasks(ctx, workspaceID, watchID)
	if err != nil {
		return err
	}
	if manager, ok := pluginsdk.PluginOwnedTaskTrees(s.host); ok {
		for _, taskID := range tasks {
			if _, err := manager.Delete(ctx, taskID); err != nil {
				return fmt.Errorf("watch: cascading delete for task %s: %w", taskID, err)
			}
		}
	}
	if err := s.host.DeleteState(ctx, watchScope, watchStateID(workspaceID, watchID), tasksKey); err != nil {
		return fmt.Errorf("watch: deleting task index: %w", err)
	}
	return s.removeWatchFromList(ctx, workspaceID, watchID)
}

// ClearWorkspace removes all watches and their plugin-owned task trees.
func (s *Service) ClearWorkspace(ctx context.Context, workspaceID string) error {
	watches, err := s.ListWatches(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, w := range watches {
		if err := s.DeleteWatch(ctx, workspaceID, w.ID); err != nil {
			return err
		}
	}
	return s.host.DeleteState(ctx, workspaceScope, workspaceID, watchesKey)
}

// Poll fetches issues in watch's project, creates one task per newly
// matching, not-yet-seen issue, subject to the maxInflightTasks throttle. A
// disabled watch is a no-op.
func (s *Service) Poll(ctx context.Context, w Watch, issuesSvc *issues.Service) error {
	if !w.Enabled {
		return nil
	}

	for offset := 0; ; {
		result, err := issuesSvc.ListIssues(ctx, issues.ListIssuesParams{ProjectID: strconv.Itoa(w.ProjectID), Offset: offset, Limit: 100})
		if err != nil {
			return err
		}
		for _, issue := range result.Issues {
			if !w.matches(issue) {
				continue
			}
			seen, err := s.hasSeen(ctx, w.WorkspaceID, w.ID, issue.ID)
			if err != nil {
				return err
			}
			if seen {
				continue
			}
			if w.MaxInflightTasks > 0 {
				inflight, err := s.inflightCount(ctx, w)
				if err != nil {
					return err
				}
				if inflight >= w.MaxInflightTasks {
					continue // leave unseen: retry once a slot frees up
				}
			}
			if err := s.createTask(ctx, w, issue); err != nil {
				return err
			}
		}
		offset += len(result.Issues)
		if len(result.Issues) == 0 || offset >= result.TotalCount {
			return nil
		}
	}
}

func (s *Service) createTask(ctx context.Context, w Watch, issue issues.Issue) error {
	task, err := s.host.Tasks().Create(ctx, pluginsdk.CreateTaskInput{
		WorkspaceID: w.WorkspaceID,
		Title:       fmt.Sprintf("Redmine #%d: %s", issue.ID, issue.Subject),
		Description: issue.Description,
		Metadata: map[string]any{
			metadataKeyWatchID: w.ID,
			metadataKeyIssueID: issue.ID,
		},
	})
	if err != nil {
		return fmt.Errorf("watch: creating task for issue %d: %w", issue.ID, err)
	}
	return s.recordWatchTask(ctx, w.WorkspaceID, w.ID, issue.ID, task.ID)
}

// inflightCount counts this watch's created tasks that are not yet in a
// terminal state — the throttle's live enforcement point. It reads
// metadataKeyWatchID back from each task, but only tasks this package's own
// index already associates with the watch are examined (the index is the
// authoritative per-watch task list; the metadata key is what a mismatch
// bug would silently break — see the constant's doc comment).
func (s *Service) inflightCount(ctx context.Context, w Watch) (int, error) {
	tasks, err := s.watchTasks(ctx, w.WorkspaceID, w.ID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, taskID := range tasks {
		task, err := s.host.Tasks().Get(ctx, taskID)
		if err != nil {
			continue // deleted/not found: does not count as inflight
		}
		if task.Metadata[metadataKeyWatchID] != w.ID {
			continue
		}
		if !terminalTaskStates[task.State] {
			count++
		}
	}
	return count, nil
}

func (s *Service) hasSeen(ctx context.Context, workspaceID, watchID string, issueID int) (bool, error) {
	tasks, err := s.watchTasks(ctx, workspaceID, watchID)
	if err != nil {
		return false, err
	}
	_, ok := tasks[issueID]
	return ok, nil
}

func watchStateID(workspaceID, watchID string) string { return workspaceID + ":" + watchID }

func (s *Service) watchTasks(ctx context.Context, workspaceID, watchID string) (map[int]string, error) {
	value, found, err := s.host.GetState(ctx, watchScope, watchStateID(workspaceID, watchID), tasksKey)
	if err != nil {
		return nil, fmt.Errorf("watch: reading task index: %w", err)
	}
	out := make(map[int]string)
	if !found {
		return out, nil
	}
	raw, _ := value["issue_id_to_task_id"].(map[string]any)
	for issueIDStr, taskID := range raw {
		issueID, err := strconv.Atoi(issueIDStr)
		if err != nil {
			continue
		}
		if id, ok := taskID.(string); ok {
			out[issueID] = id
		}
	}
	return out, nil
}

func (s *Service) recordWatchTask(ctx context.Context, workspaceID, watchID string, issueID int, taskID string) error {
	tasks, err := s.watchTasks(ctx, workspaceID, watchID)
	if err != nil {
		return err
	}
	tasks[issueID] = taskID
	values := make(map[string]any, len(tasks))
	for id, tID := range tasks {
		values[strconv.Itoa(id)] = tID
	}
	if err := s.host.SetState(ctx, watchScope, watchStateID(workspaceID, watchID), tasksKey, map[string]any{"issue_id_to_task_id": values}); err != nil {
		return fmt.Errorf("watch: saving task index: %w", err)
	}
	return nil
}

func (s *Service) saveWatchInList(ctx context.Context, w Watch) error {
	watches, err := s.ListWatches(ctx, w.WorkspaceID)
	if err != nil {
		return err
	}
	replaced := false
	for i, existing := range watches {
		if existing.ID == w.ID {
			watches[i] = w
			replaced = true
			break
		}
	}
	if !replaced {
		watches = append(watches, w)
	}
	return s.saveWatchList(ctx, w.WorkspaceID, watches)
}

func (s *Service) removeWatchFromList(ctx context.Context, workspaceID, watchID string) error {
	watches, err := s.ListWatches(ctx, workspaceID)
	if err != nil {
		return err
	}
	kept := make([]Watch, 0, len(watches))
	for _, w := range watches {
		if w.ID != watchID {
			kept = append(kept, w)
		}
	}
	return s.saveWatchList(ctx, workspaceID, kept)
}

func (s *Service) saveWatchList(ctx context.Context, workspaceID string, watches []Watch) error {
	values := make([]any, len(watches))
	for i, w := range watches {
		values[i] = w.toMap()
	}
	if err := s.host.SetState(ctx, workspaceScope, workspaceID, watchesKey, map[string]any{"watches": values}); err != nil {
		return fmt.Errorf("watch: saving watches: %w", err)
	}
	return nil
}

func (w Watch) toMap() map[string]any {
	m := map[string]any{
		"id":                 w.ID,
		"project_id":         w.ProjectID,
		"max_inflight_tasks": w.MaxInflightTasks,
		"enabled":            w.Enabled,
	}
	if w.TrackerID != nil {
		m["tracker_id"] = *w.TrackerID
	}
	if w.StatusID != nil {
		m["status_id"] = *w.StatusID
	}
	return m
}

func watchFromMap(workspaceID string, m map[string]any) Watch {
	w := Watch{WorkspaceID: workspaceID}
	if v, ok := m["id"].(string); ok {
		w.ID = v
	}
	if v, ok := m["project_id"].(float64); ok {
		w.ProjectID = int(v)
	}
	if v, ok := m["max_inflight_tasks"].(float64); ok {
		w.MaxInflightTasks = int(v)
	}
	if v, ok := m["enabled"].(bool); ok {
		w.Enabled = v
	}
	if v, ok := m["tracker_id"].(float64); ok {
		id := int(v)
		w.TrackerID = &id
	}
	if v, ok := m["status_id"].(float64); ok {
		id := int(v)
		w.StatusID = &id
	}
	return w
}

func newWatchID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("watch: generating id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
