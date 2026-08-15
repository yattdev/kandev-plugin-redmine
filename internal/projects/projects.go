// Package projects owns Redmine project listing (walking
// /projects.json's offset/limit pagination to exhaustion) and the persisted
// per-workspace selected-project set that internal/sync and internal/watch
// scope their polling to.
package projects

import (
	"context"
	"fmt"
	"sort"

	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const pageSize = 100

const (
	stateScope = "workspace"
	stateKey   = "projects"
)

type Service struct {
	host pluginsdk.Host
}

func New(host pluginsdk.Host) *Service {
	return &Service{host: host}
}

// ListLive fetches every project from the connected instance, walking
// offset/limit=100 pages until offset+len(items) >= total_count (a
// 250-project instance makes three requests: offset 0, 100, 200).
func (s *Service) ListLive(ctx context.Context, client *redmineclient.Client) ([]redmineclient.Project, error) {
	var all []redmineclient.Project
	offset := 0
	for {
		items, total, err := client.ListProjectsPage(ctx, offset, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		offset += len(items)
		if len(items) == 0 || offset >= total {
			break
		}
	}
	return all, nil
}

// SaveSelection persists which projects sync for workspaceID.
func (s *Service) SaveSelection(ctx context.Context, workspaceID string, projectIDs []int) error {
	sorted := append([]int(nil), projectIDs...)
	sort.Ints(sorted)
	ids := make([]any, len(sorted))
	for i, id := range sorted {
		ids[i] = id
	}
	if err := s.host.SetState(ctx, stateScope, workspaceID, stateKey, map[string]any{"project_ids": ids}); err != nil {
		return fmt.Errorf("projects: saving selection: %w", err)
	}
	return nil
}

// GetSelection returns the persisted project IDs for workspaceID (empty if
// none saved yet).
func (s *Service) GetSelection(ctx context.Context, workspaceID string) ([]int, error) {
	value, found, err := s.host.GetState(ctx, stateScope, workspaceID, stateKey)
	if err != nil {
		return nil, fmt.Errorf("projects: reading selection: %w", err)
	}
	if !found {
		return nil, nil
	}
	raw, _ := value["project_ids"].([]any)
	ids := make([]int, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			ids = append(ids, int(f))
		}
	}
	return ids, nil
}
