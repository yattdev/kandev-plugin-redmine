// Package main is the backend half of the kandev-plugin-redmine plugin. It
// implements pluginsdk.Plugin (see redminePlugin below) and is spawned by
// kandev as a gRPC subprocess — there is no HTTP server, no listen address,
// and no secrets to configure: pluginsdk.Serve owns the entire transport.
//
// redminePlugin starts minimal (this file) and grows one optional interface
// per plan task: pluginsdk.ActionHandler (settings UI backend calls, task
// 03+), pluginsdk.EntityReferenceSearcher/EntityReferenceAuthorizer (composer
// `#` mentions, task 06), and Host.PluginOwnedTaskTrees() consumption
// (watcher task cascade delete, task 07). See
// docs/plans/redmine-plugin/plan.md in kdlbs/kandev for the full breakdown.
package main

import "github.com/kandev/kandev/pkg/pluginsdk"

// redminePlugin implements pluginsdk.Plugin via pluginsdk.UnimplementedPlugin.
// It declares no `events` or `webhooks` capability (manifest.yaml), so the
// embedded no-op OnEvent/HandleWebhook are never exercised in practice; they
// stay unoverridden until a task needs them.
type redminePlugin struct {
	pluginsdk.UnimplementedPlugin
}

var _ pluginsdk.Plugin = (*redminePlugin)(nil)
