// Package main is the backend half of the kandev-plugin-redmine plugin. It
// implements pluginsdk.Plugin (redminePlugin) and is spawned by kandev as a
// gRPC subprocess — there is no HTTP server, no listen address, and no
// secrets to configure: pluginsdk.Serve owns the entire transport.
//
// redminePlugin composes one Service per domain package (connection,
// projects, fieldmapping, tasklink, sync, watch) and wires them together:
// SetHost is the plugin's only lifecycle hook (called once, after the
// go-plugin broker connection completes), so it both injects the Host into
// every service and starts the plugin's own background loops — there is no
// host healthpoll/watchreset equivalent for plugins (plan Risks), so this
// plugin owns polling entirely.
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

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// syncPollInterval is the inbound-sync / write-back-reconciliation cadence.
// Outbound write-back also has a near-real-time path via OnEvent
// ("task.moved"); this poll is the backstop for missed events and the only
// path for inbound changes (see internal/sync's package doc comment).
const syncPollInterval = 60 * time.Second

type redminePlugin struct {
	pluginsdk.UnimplementedPlugin

	mu    sync.Mutex
	ready bool
	wg    sync.WaitGroup

	connectionSvc   *connection.Service
	projectsSvc     *projects.Service
	fieldmappingSvc *fieldmapping.Service
	tasklinkSvc     *tasklink.Service
	syncSvc         *redminesync.Service
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
	if p.ready {
		return
	}

	p.connectionSvc = connection.New(host)
	p.projectsSvc = projects.New(host)
	p.fieldmappingSvc = fieldmapping.New(host)
	p.tasklinkSvc = tasklink.New(host)
	p.syncSvc = redminesync.New(host, p.tasklinkSvc)
	p.healthPoller = connection.NewHealthPoller(p.connectionSvc)
	p.ready = true

	ctx := context.Background()
	p.healthPoller.Start(ctx)
	p.wg.Add(1)
	go p.runSyncLoop(ctx)
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

	client, err := p.connectionSvc.Client(ctx, workspaceID)
	if err != nil {
		return nil // not connected (or credentials rejected) - nothing to poll
	}
	issuesSvc := issues.New(client)

	return p.syncSvc.PollInbound(ctx, workspaceID, issuesSvc, mapping, projectIDs, opts)
}

// OnEvent handles task.moved for near-real-time outbound write-back (see
// manifest.yaml's capabilities.events doc comment for why this plugin
// declares an events capability at all). Every other event type is ignored;
// events delivery is best-effort, so a missed task.moved still converges on
// the next sync poll (pollWorkspace calls PollInbound only — write-back
// reconciliation for a missed event happens on the *next task.moved* or via
// the manual "Set Redmine status" action, since inferring "this step is
// mapped and doesn't match Redmine" from polling alone would require
// fetching every linked task's current step on every tick; OnEvent is the
// intended primary path and is expected to be reliable in the common case).
func (p *redminePlugin) OnEvent(ctx context.Context, e *pluginsdk.Event) error {
	if e == nil || e.EventType != "task.moved" {
		return nil
	}
	p.mu.Lock()
	ready := p.ready
	p.mu.Unlock()
	if !ready {
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
