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

## Required behavior

1. Validate the URL and credentials before saving a connection; distinguish
   invalid credentials, an API-disabled instance, and an unreachable instance.
2. List all visible projects using Redmine's `offset`/`limit` pagination and
   persist the selected project IDs.
3. Fetch statuses, trackers, priorities, and custom fields from the connected
   Redmine instance rather than hardcoding instance-specific values. If custom
   fields cannot be listed, derive them from recent issues.
4. Create and update issues, including Redmine's upload-token attachment flow.
5. Persist task-to-issue links, support unlinking, and expose the shared
   Kandev task-link dialog.
6. Poll closed as well as open issues by always sending `status_id=*`. Persist
   an inclusive `updated_on>=cursor` query cursor with a one-second overlap.
7. Apply mapped inbound status updates; optionally synchronize title and
   description. A plugin-originated outbound status must not bounce back as an
   inbound task transition.
8. Write a mapped status on `task.moved` only when automatic write-back is
   enabled. Manual write-back remains available when it is disabled.
9. Search Redmine issues in the composer and reauthorize a selected reference
   immediately before submission.
10. Create watcher tasks once per matching issue, automatically link them to
    the originating Redmine issue, deduplicate them, enforce the configured
    inflight cap, and delete owned task trees when a watch or its connection
    is removed. Disconnect before uninstall: host uninstall stops
    the plugin and purges its state/secrets, but has no pre-uninstall hook to
    delete plugin-owned task trees.
11. Preserve state on Redmine failures, retry with bounded backoff, show
    degraded health, and resume cleanly after reconnecting or re-enabling the
    plugin.

## Host contract and automated evidence

| Scenario | Plugin entry point | Host contract | Automated evidence |
| --- | --- | --- | --- |
| Connection validation, rotation, revocation, redaction, workspace isolation | `connection.save/get/disconnect` | verified workspace action context; secrets and state RPCs | `internal/connection/connection_test.go`, `server/actions_test.go` |
| Health and retry behavior | `connection.HealthPoller` | state/secrets RPCs | `internal/connection/healthpoll_test.go`, `internal/redmineclient/client_test.go` |
| Project pagination and selected projects | `projects.list/save` | workspace action context; state RPC | `internal/projects/projects_test.go` |
| Live field mapping and custom-field fallback | `fieldmapping.get/save` | workspace action context; workflows read RPC | `internal/fieldmapping/fieldmapping_test.go` |
| Issue search, writes, and upload tokens | Redmine client and issue service | authenticated action/reference context | `internal/redmineclient/search_test.go`, `internal/issues/issues_test.go` |
| Link, unlink, and native link surface | `link.*`; UI registration | task action and `openTaskLinkDialog` | `internal/tasklink/tasklink_test.go`, `server/actions_test.go`, `server/plugin_test.go` |
| Closed-status inbound sync, cursor, title/description option, echo suppression | sync poller | task read/write RPCs | `internal/sync/sync_test.go` |
| Automatic and manual status write-back | `OnEvent`; `link.set_status` | `task.moved` event; task action context | `server/plugin_test.go`, `internal/sync/sync_test.go` |
| Watch filters, deduplication, throttling, cleanup | `watches.*` and poller | state RPCs; `PluginOwnedTaskTrees` | `internal/watch/watch_test.go`, `server/actions_watch.go` |
| Composer references and submit-time authorization | reference searcher/authorizer | `reference_sources` SDK contract | `server/references_test.go` |
| Packaged native UI registration and lifecycle | `ui/bundle.js`, manifest | integration settings/action registration | host packaged-plugin E2E: `apps/web/e2e/tests/plugins/redmine-packaged-plugin.spec.ts` |

## Integration gate

Before publishing a non-preview release, install the packaged plugin into a
compatible Kandev instance and run the scenarios above against a disposable
Redmine instance. Use two Kandev workspaces to prove connection isolation.
Record the Kandev and Redmine versions, package checksum, exact commands, test
data, and any scenario that remains manual. The author then performs final
acceptance testing; a release is not author-accepted merely because unit or
package tests pass.
