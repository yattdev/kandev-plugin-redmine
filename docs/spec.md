# Redmine plugin specification and verification trace

`kandev-plugin-redmine` is the canonical implementation of the Redmine
integration. It connects each Kandev workspace independently to a Redmine
instance by API key, and it must not put a credential, a workspace identifier,
or a task identifier supplied by an untrusted UI request into another
workspace's state.

## Connection and ownership

- A connection consists of an HTTP(S) base URL and an API key sent only in
  `X-Redmine-API-Key`.
- Each workspace has its own credential and state. Credentials are encrypted
  at rest by Kandev's host-managed secret store; the plugin only composes a
  workspace-specific secret key. Rotating or
  disconnecting one connection cannot affect another workspace.
- API keys are write-only: action responses, plugin state, logs, and task
  links must never disclose them.
- v0.1 plugin-encrypted secrets are dual-read for upgrade compatibility and
  rewritten through the host store on a best-effort read migration.
- The settings UI uses authenticated manifest actions. The backend always uses
  the host-verified action context for workspace and task scope.
- Redmine has no core webhooks. Health, inbound synchronization, and watches
  are plugin-owned polling work.
- A Redmine administrator account is not required. A normal user's API key
  lists only projects and issues visible to that user; write operations depend
  on that user's ordinary project-role permissions. Admin-only custom-field
  listing may return 403 and must use the issue-derived fallback.
- The workspace enable preference is independent of the connection. Disabling
  pauses background polling and automatic write-back without deleting the
  workspace secret or saved configuration.

## Required behavior

1. Validate the URL and credentials before saving a connection; distinguish
   invalid credentials, an API-disabled instance, and an unreachable instance.
2. List all visible projects using Redmine's `offset`/`limit` pagination and
   persist the selected project IDs.
3. Fetch statuses, trackers, priorities, and custom fields from the connected
   Redmine instance rather than hardcoding instance-specific values. The user
   selects a workspace workflow, explicitly adds live Redmine statuses, and
   selects a Kandev step for each added status. If custom fields cannot be
   listed, derive them from recent issues.
4. Create and update issues, including Redmine's upload-token attachment flow.
5. Persist task-to-issue links, support unlinking, and expose the shared
   Kandev task-link dialog.
6. Poll closed as well as open issues by always sending `status_id=*`. Persist
   an inclusive `updated_on>=cursor` query cursor with a one-second overlap.
7. Apply mapped inbound status updates; optionally synchronize title and
   description. A plugin-originated outbound status must not bounce back as an
   inbound task transition.
8. Apply configured tracker and priority mappings both at watcher task
   creation (initial labels and priority) and during inbound synchronization
   of already-linked tasks. The inbound reconciliation preserves user-set
   labels in exact order and content: it removes only the plugin-owned
   tracker label recorded on the link, and adds the currently mapped one. The
   plugin-owned label marker is persisted only after a successful task
   update, or repaired in place when the task already reflects the desired
   label but the marker is stale. An empty or unassigned tracker mapping
   removes only the previously owned tracker label.
9. Write a mapped status on `task.moved` only when automatic write-back is
   enabled. Manual write-back remains available when it is disabled.
10. Search Redmine issues in the composer and reauthorize a selected reference
    immediately before submission.
11. Create watcher tasks once per matching issue, automatically link them to
    the originating Redmine issue, deduplicate them, enforce the configured
    inflight cap, and scope live tracker/status/priority/assignee/category and
    custom-field filters to each selected Redmine project. Refresh those live
    choices from Redmine before saving. Delete owned task trees when a watch
    or its connection is removed. Disconnect before uninstall: host uninstall stops
    the plugin and purges its state/secrets, but has no pre-uninstall hook to
    delete plugin-owned task trees.
12. Preserve state on Redmine failures, retry with bounded backoff, show
    degraded health, and resume cleanly after reconnecting or re-enabling the
    plugin.

## Host contract and automated evidence

| Scenario | Plugin entry point | Host contract | Automated evidence |
| --- | --- | --- | --- |
| Connection validation, rotation, revocation, redaction, workspace isolation | `connection.save/get/disconnect` | verified workspace action context; secrets and state RPCs | `internal/connection/connection_test.go`, `server/actions_test.go` |
| Workspace enable/disable without credential loss | `integration.enabled.get/save` | verified workspace action context; state RPC | `internal/connection/connection_test.go`, `server/actions_test.go`, `ui/e2e/live-redmine.spec.ts` |
| Health and retry behavior | `connection.HealthPoller` | state/secrets RPCs | `internal/connection/healthpoll_test.go`, `internal/redmineclient/client_test.go` |
| Project pagination and selected projects | `projects.list/save` | workspace action context; state RPC | `internal/projects/projects_test.go` |
| Live field mapping and custom-field fallback | `fieldmapping.get/save` | workspace action context; workflows read RPC | `internal/fieldmapping/fieldmapping_test.go` |
| Issue search, writes, and upload tokens | Redmine client and issue service | authenticated action/reference context | `internal/redmineclient/search_test.go`, `internal/issues/issues_test.go` |
| Link, unlink, and native link surface | `link.*`; UI registration | task action and `openTaskLinkDialog` | `internal/tasklink/tasklink_test.go`, `server/actions_test.go`, `server/plugin_test.go` |
| Closed-status inbound sync, cursor, title/description option, echo suppression | sync poller | task read/write RPCs | `internal/sync/sync_test.go` |
| Automatic and manual status write-back | `OnEvent`; `link.set_status` | `task.moved` event; task action context | `server/plugin_test.go`, `internal/sync/sync_test.go` |
| Watch filters, deduplication, throttling, cleanup | `watches.*` and poller | state RPCs; namespaced task metadata; `PluginOwnedTaskTrees` | `internal/watch/watch_test.go`, `server/actions_test.go`, `ui/e2e/live-redmine.spec.ts` |
| Composer references and submit-time authorization | reference searcher/authorizer | `reference_sources` SDK contract | `server/references_test.go` |
| Packaged native UI registration and lifecycle | `ui/bundle.js`, manifest | integration settings/action registration | plugin-owned `ui/e2e/packaged-plugin.spec.ts`, `ui/e2e/live-redmine.spec.ts` |

## Integration gate

Before publishing a non-preview release, install the packaged plugin into a
compatible Kandev instance and run the scenarios above against a disposable
Redmine instance. Use two Kandev workspaces to prove connection isolation.
Record the Kandev and Redmine versions, package checksum, exact commands, test
data, and any scenario that remains manual. The author then performs final
acceptance testing; a release is not author-accepted merely because unit or
package tests pass.
