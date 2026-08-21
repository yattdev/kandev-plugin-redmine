// Package main is the backend half of the kandev-plugin-redmine plugin. It
// implements pluginsdk.Plugin (redminePlugin) and is spawned by kandev as a
// gRPC subprocess — there is no HTTP server, no listen address, and no
// secrets to configure: pluginsdk.Serve owns the entire transport.
//
// redminePlugin composes one Service per domain package (connection,
// projects, fieldmapping, tasklink, sync, watch) and wires them together:
// SetHost is the plugin's only public lifecycle hook (called once, after the
// go-plugin broker connection completes), so it both injects the Host into
// every service and starts the plugin's own background loops. The SDK has no
// public shutdown callback: production shutdown is process termination; the
// internal stop method below exists for tests and controlled in-process use.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"kandev-plugin-redmine/internal/connection"
	"kandev-plugin-redmine/internal/fieldmapping"
	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/projects"
	redminesync "kandev-plugin-redmine/internal/sync"
	"kandev-plugin-redmine/internal/tasklink"
	"kandev-plugin-redmine/internal/watch"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// syncPollInterval is the inbound-sync cadence. Outbound status changes are
// sent only by the task.moved event path or the manual status action.
const syncPollInterval = 60 * time.Second

type redminePlugin struct {
	pluginsdk.UnimplementedPlugin

	mu       sync.Mutex
	ready    bool
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	stopDone chan struct{}

	connectionSvc   *connection.Service
	projectsSvc     *projects.Service
	fieldmappingSvc *fieldmapping.Service
	tasklinkSvc     *tasklink.Service
	syncSvc         *redminesync.Service
	watchSvc        *watch.Service
	healthPoller    *connection.HealthPoller
}

var (
	_ pluginsdk.Plugin        = (*redminePlugin)(nil)
	_ pluginsdk.ActionHandler = (*redminePlugin)(nil)
)

// SetHost wires every service to the injected Host and starts the health
// poll and sync poll loops. Called exactly once by pluginsdk.Serve, from a
// background goroutine.
func (p *redminePlugin) SetHost(host pluginsdk.Host) {
	p.UnimplementedPlugin.SetHost(host)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ready || p.stopDone != nil {
		return
	}

	p.connectionSvc = connection.New(host)
	p.projectsSvc = projects.New(host)
	p.fieldmappingSvc = fieldmapping.New(host)
	p.tasklinkSvc = tasklink.New(host)
	p.syncSvc = redminesync.New(host, p.tasklinkSvc)
	p.watchSvc = watch.New(host)
	p.healthPoller = connection.NewHealthPoller(p.connectionSvc)
	p.ready = true

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.healthPoller.Start(p.ctx)
	p.wg.Add(1)
	go p.runSyncLoop(p.ctx)
}

// stop terminates plugin-owned goroutines. It is intentionally not part of
// the public SDK contract: Kandev ends a production plugin by terminating its
// subprocess. Tests call stop through t.Cleanup so in-process test plugins do
// not outlive their test.
func (p *redminePlugin) stop() {
	p.mu.Lock()
	if p.stopDone != nil {
		done := p.stopDone
		p.mu.Unlock()
		<-done
		return
	}
	if !p.ready {
		p.mu.Unlock()
		return
	}
	done := make(chan struct{})
	p.stopDone = done
	cancel := p.cancel
	healthPoller := p.healthPoller
	p.ready = false
	p.mu.Unlock()

	cancel()
	healthPoller.Stop()
	p.wg.Wait()

	p.mu.Lock()
	close(done)
	p.mu.Unlock()
}

func (p *redminePlugin) runSyncLoop(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(syncPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAllWorkspaces(ctx)
		}
	}
}

func (p *redminePlugin) pollAllWorkspaces(ctx context.Context) {
	ids, err := p.connectionSvc.ListWorkspaceIDs(ctx)
	if err != nil {
		log.Printf("redmine: listing connected workspaces: %v", err)
		return
	}
	for _, workspaceID := range ids {
		if err := p.pollWorkspace(ctx, workspaceID); err != nil {
			log.Printf("redmine: sync poll for workspace %s: %v", workspaceID, err)
		}
	}
}

func (p *redminePlugin) pollWorkspace(ctx context.Context, workspaceID string) error {
	client, err := p.connectionSvc.Client(ctx, workspaceID)
	if err != nil {
		return nil // not connected (or credentials rejected) - nothing to poll
	}
	issuesSvc := issues.New(client)

	if err := p.pollSync(ctx, workspaceID, issuesSvc); err != nil {
		return err
	}
	return p.pollWatches(ctx, workspaceID, issuesSvc)
}

func (p *redminePlugin) pollSync(ctx context.Context, workspaceID string, issuesSvc *issues.Service) error {
	projectIDs, err := p.projectsSvc.GetSelection(ctx, workspaceID)
	if err != nil || len(projectIDs) == 0 {
		return err
	}

	mapping, found, err := p.fieldmappingSvc.Get(ctx, workspaceID)
	if err != nil || !found {
		return err
	}

	opts, err := p.syncSvc.GetOptions(ctx, workspaceID)
	if err != nil {
		return err
	}

	return p.syncSvc.PollInbound(ctx, workspaceID, issuesSvc, mapping, projectIDs, opts)
}

func (p *redminePlugin) pollWatches(ctx context.Context, workspaceID string, issuesSvc *issues.Service) error {
	watches, err := p.watchSvc.ListWatches(ctx, workspaceID)
	if err != nil {
		return err
	}
	mapping, mappingFound, err := p.fieldmappingSvc.Get(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, w := range watches {
		if mappingFound && needsWatchBackfill(w) {
			w = applyWatchMapping(w, mapping)
			if err := p.watchSvc.UpdateWatch(ctx, w); err != nil {
				return err
			}
		}
		if err := p.watchSvc.Poll(ctx, w, issuesSvc); err != nil {
			return err
		}
	}
	return nil
}

func needsWatchBackfill(w watch.Watch) bool {
	return w.WorkflowID == "" || (w.StatusID != nil && w.WorkflowStepID == "") || w.TrackerLabels == nil || w.PriorityMappings == nil
}

func applyWatchMapping(w watch.Watch, mapping fieldmapping.Mapping) watch.Watch {
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
	return w
}

// OnEvent handles task.moved for near-real-time outbound write-back (see
// manifest.yaml's capabilities.events doc comment for why this plugin
// declares an events capability at all). Every other event type is ignored.
// Polling is inbound only: a missed task.moved is not reconstructed by a
// later poll and can instead be corrected with another move or the manual
// "Set Redmine status" action.
func (p *redminePlugin) OnEvent(ctx context.Context, e *pluginsdk.Event) error {
	if e == nil {
		return nil
	}
	p.mu.Lock()
	ready := p.ready
	p.mu.Unlock()
	if !ready {
		return nil
	}
	if e.EventType == "workspace.deleted" {
		workspaceID := e.WorkspaceID
		if workspaceID == "" {
			workspaceID, _ = e.Payload["workspace_id"].(string)
		}
		if workspaceID == "" {
			return nil
		}
		return p.clearWorkspace(ctx, workspaceID)
	}
	if e.EventType == "task.deleted" {
		taskID, _ := e.Payload["task_id"].(string)
		if taskID == "" {
			return nil
		}
		return p.tasklinkSvc.Unset(ctx, taskID)
	}
	if e.EventType != "task.moved" {
		return nil
	}

	taskID, _ := e.Payload["task_id"].(string)
	toStepID, _ := e.Payload["to_step_id"].(string)
	if taskID == "" || toStepID == "" {
		return nil
	}

	link, found, err := p.tasklinkSvc.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("redmine: OnEvent task.moved: reading link for task %s: %w", taskID, err)
	}
	if !found {
		return nil
	}

	mapping, found, err := p.fieldmappingSvc.Get(ctx, link.WorkspaceID)
	if err != nil {
		return fmt.Errorf("redmine: OnEvent task.moved: reading field mapping for workspace %s: %w", link.WorkspaceID, err)
	}
	if !found {
		return nil
	}

	opts, err := p.syncSvc.GetOptions(ctx, link.WorkspaceID)
	if err != nil {
		return fmt.Errorf("redmine: OnEvent task.moved: reading sync options for workspace %s: %w", link.WorkspaceID, err)
	}
	if !opts.AutoStatusWriteback {
		return nil
	}

	client, err := p.connectionSvc.Client(ctx, link.WorkspaceID)
	if err != nil {
		return nil // not connected; nothing to write back to
	}

	return p.syncSvc.PushWriteback(ctx, taskID, toStepID, mapping, issues.New(client), opts)
}

func (p *redminePlugin) clearWorkspace(ctx context.Context, workspaceID string) error {
	if err := p.watchSvc.ClearWorkspace(ctx, workspaceID); err != nil {
		return fmt.Errorf("redmine: clearing watches: %w", err)
	}
	if err := p.tasklinkSvc.ClearWorkspace(ctx, workspaceID); err != nil {
		return fmt.Errorf("redmine: clearing links: %w", err)
	}
	if err := p.projectsSvc.Clear(ctx, workspaceID); err != nil {
		return err
	}
	if err := p.fieldmappingSvc.Clear(ctx, workspaceID); err != nil {
		return err
	}
	if err := p.syncSvc.Clear(ctx, workspaceID); err != nil {
		return err
	}
	return p.connectionSvc.Disconnect(ctx, workspaceID)
}
