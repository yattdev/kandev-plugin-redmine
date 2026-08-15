// Package tasklink persists the link between a Kandev task and a Redmine
// issue, plus the echo-suppression bookkeeping internal/sync needs to keep a
// write-back round trip from bouncing the task. The host's Tasks().Update
// has no metadata field to write a link onto an existing task (see
// docs/plans/redmine-plugin/task-06-task-linking-bidirectional-sync.md
// "Plan deviations"), so the link itself is plugin-owned state — the same
// pattern kandev-plugin-bitbucket uses for its own task-scoped link/unlink
// actions.
package tasklink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// Link is the persisted state for one linked task.
type Link struct {
	IssueID     int
	IssueURL    string
	WorkspaceID string

	// Echo suppression: the most recent values this plugin itself pushed
	// outbound, compared against inbound observations before applying them
	// (spec "Bidirectional sync").
	LastPushedStatusID        *int
	LastPushedTitle           string
	LastPushedDescriptionHash string
}

const (
	taskScope      = "task"
	linkKey        = "link"
	workspaceScope = "workspace"
	indexKey       = "issue_links"
)

type Service struct {
	host pluginsdk.Host
}

func New(host pluginsdk.Host) *Service {
	return &Service{host: host}
}

// Set links taskID to a Redmine issue and adds it to the workspace's reverse
// index (issue ID -> task ID) the sync/watch loops use.
func (s *Service) Set(ctx context.Context, taskID, workspaceID string, issueID int, issueURL string) error {
	link := Link{IssueID: issueID, IssueURL: issueURL, WorkspaceID: workspaceID}
	if err := s.save(ctx, taskID, link); err != nil {
		return err
	}
	return s.addToIndex(ctx, workspaceID, issueID, taskID)
}

// Get returns the current link for taskID, if any.
func (s *Service) Get(ctx context.Context, taskID string) (*Link, bool, error) {
	value, found, err := s.host.GetState(ctx, taskScope, taskID, linkKey)
	if err != nil {
		return nil, false, fmt.Errorf("tasklink: reading link: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	link := linkFromMap(value)
	return &link, true, nil
}

// Unset removes the link and its reverse-index entry. A no-op if taskID
// isn't linked.
func (s *Service) Unset(ctx context.Context, taskID string) error {
	link, found, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if err := s.host.DeleteState(ctx, taskScope, taskID, linkKey); err != nil {
		return fmt.Errorf("tasklink: deleting link: %w", err)
	}
	if !found {
		return nil
	}
	return s.removeFromIndex(ctx, link.WorkspaceID, link.IssueID)
}

// TaskIDForIssue resolves the reverse index: which task, if any, is linked
// to issueID within workspaceID. Scoped by workspace so two workspaces'
// links never resolve into each other, even if both happen to reference the
// same numeric issue ID on two different Redmine instances.
func (s *Service) TaskIDForIssue(ctx context.Context, workspaceID string, issueID int) (string, bool, error) {
	index, err := s.index(ctx, workspaceID)
	if err != nil {
		return "", false, err
	}
	taskID, ok := index[strconv.Itoa(issueID)]
	return taskID, ok, nil
}

// RecordPushedStatus records the status this plugin itself just pushed
// outbound, for echo suppression on the next inbound poll.
func (s *Service) RecordPushedStatus(ctx context.Context, taskID string, statusID int) error {
	link, found, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("tasklink: task %s is not linked", taskID)
	}
	link.LastPushedStatusID = &statusID
	return s.save(ctx, taskID, *link)
}

// RecordPushedTitleAndDescription records the title/description this plugin
// itself just pushed outbound, for echo suppression on the next inbound
// poll. The description is stored only as a hash (it is otherwise duplicated
// in the task row itself; no need to keep a second full copy in plugin
// state).
func (s *Service) RecordPushedTitleAndDescription(ctx context.Context, taskID, title, description string) error {
	link, found, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("tasklink: task %s is not linked", taskID)
	}
	link.LastPushedTitle = title
	link.LastPushedDescriptionHash = HashDescription(description)
	return s.save(ctx, taskID, *link)
}

// HashDescription is the description-comparison hash used for echo
// suppression, exported so internal/sync can compute the same hash for an
// inbound description before comparing it against LastPushedDescriptionHash.
func HashDescription(description string) string {
	sum := sha256.Sum256([]byte(description))
	return hex.EncodeToString(sum[:])
}

func (s *Service) save(ctx context.Context, taskID string, link Link) error {
	if err := s.host.SetState(ctx, taskScope, taskID, linkKey, link.toMap()); err != nil {
		return fmt.Errorf("tasklink: saving link: %w", err)
	}
	return nil
}

func (s *Service) index(ctx context.Context, workspaceID string) (map[string]string, error) {
	value, found, err := s.host.GetState(ctx, workspaceScope, workspaceID, indexKey)
	if err != nil {
		return nil, fmt.Errorf("tasklink: reading issue index: %w", err)
	}
	index := make(map[string]string)
	if !found {
		return index, nil
	}
	raw, _ := value["issue_id_to_task_id"].(map[string]any)
	for issueID, taskID := range raw {
		if id, ok := taskID.(string); ok {
			index[issueID] = id
		}
	}
	return index, nil
}

func (s *Service) addToIndex(ctx context.Context, workspaceID string, issueID int, taskID string) error {
	index, err := s.index(ctx, workspaceID)
	if err != nil {
		return err
	}
	index[strconv.Itoa(issueID)] = taskID
	return s.saveIndex(ctx, workspaceID, index)
}

func (s *Service) removeFromIndex(ctx context.Context, workspaceID string, issueID int) error {
	index, err := s.index(ctx, workspaceID)
	if err != nil {
		return err
	}
	delete(index, strconv.Itoa(issueID))
	return s.saveIndex(ctx, workspaceID, index)
}

func (s *Service) saveIndex(ctx context.Context, workspaceID string, index map[string]string) error {
	values := make(map[string]any, len(index))
	for issueID, taskID := range index {
		values[issueID] = taskID
	}
	if err := s.host.SetState(ctx, workspaceScope, workspaceID, indexKey, map[string]any{"issue_id_to_task_id": values}); err != nil {
		return fmt.Errorf("tasklink: saving issue index: %w", err)
	}
	return nil
}

func (l Link) toMap() map[string]any {
	m := map[string]any{
		"issue_id":     l.IssueID,
		"issue_url":    l.IssueURL,
		"workspace_id": l.WorkspaceID,
	}
	if l.LastPushedStatusID != nil {
		m["last_pushed_status_id"] = *l.LastPushedStatusID
	}
	if l.LastPushedTitle != "" {
		m["last_pushed_title"] = l.LastPushedTitle
	}
	if l.LastPushedDescriptionHash != "" {
		m["last_pushed_description_hash"] = l.LastPushedDescriptionHash
	}
	return m
}

func linkFromMap(m map[string]any) Link {
	link := Link{}
	if v, ok := m["issue_id"].(float64); ok {
		link.IssueID = int(v)
	}
	if v, ok := m["issue_url"].(string); ok {
		link.IssueURL = v
	}
	if v, ok := m["workspace_id"].(string); ok {
		link.WorkspaceID = v
	}
	if v, ok := m["last_pushed_status_id"].(float64); ok {
		statusID := int(v)
		link.LastPushedStatusID = &statusID
	}
	if v, ok := m["last_pushed_title"].(string); ok {
		link.LastPushedTitle = v
	}
	if v, ok := m["last_pushed_description_hash"].(string); ok {
		link.LastPushedDescriptionHash = v
	}
	return link
}
