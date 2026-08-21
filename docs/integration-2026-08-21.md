# Disposable integration run — 2026-08-21

This is a task-owned, non-production verification run for the corrected
v0.2.0 candidate. It is automated evidence, not author acceptance.

## Environment

- Plugin: `yattdev/kandev-plugin-redmine`, candidate branch
  `feature/complete-redmine-plu-9ux`, package
  `kandev-plugin-redmine-0.2.0.tar.gz`.
- Kandev: provider-neutral candidate commit
  `dcfe7c400719f0fc287b4e79e4b08ecb7afe717c`, isolated SQLite database,
  listening on `0.0.0.0:13081`.
- Redmine: official Redmine 6.0.10 container
  `redmine-it-7ca86e53`, isolated storage, REST API enabled, listening on
  `0.0.0.0:13080`.
- Data: one selected project, more than 100 issues (including closed issues),
  live statuses/trackers/priorities, a custom field, and two disposable admin
  API keys used to exercise credential rotation.
- Workspace isolation: the default workspace plus a second workspace created
  and deleted by the acceptance run.

No API key is recorded in this document, screenshots, test output, plugin
state, task metadata, or action responses.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| Go behavior and race safety | PASS | `go test ./... -race` across connection, client, issues, mappings, links, sync, watches, and server actions. |
| Static and UI contracts | PASS | `go vet ./...`; `npm run test:ui-contract` (7/7). |
| Five-platform package | PASS | `make verify-package`; manifest, five executables, UI bundle, and checksums verified; test sources excluded from the runtime archive. |
| Packaged native UI | PASS | `npm run e2e`; real tarball installed into disposable Kandev and unconfigured settings UI rendered safely. |
| Connection and rotation | PASS | Two valid API keys saved sequentially; both validated live; neither appeared in a response; the second workspace remained disconnected. |
| Projects and live mappings | PASS | Visible projects loaded and selected; statuses, trackers, priorities, and custom-field evidence loaded from Redmine; status/tracker/priority mappings persisted. |
| Issue write and attachment | PASS | Binary upload returned token+filename+content type; issue create/update succeeded; Redmine returned the attached filename. |
| Durable link and manual write-back | PASS | Task linked by `#issue`; link re-read; manual status push succeeded; unlink succeeded. |
| Closed-status inbound sync | PASS | A linked issue moved to a live `is_closed` Redmine status; the inclusive `status_id=*` poll moved the task to the mapped workflow step and updated its title. |
| Restart/cursor recovery | PASS | Plugin disable/re-enable retained connection, mapping, link, options, and cursor; a subsequent Redmine update synchronized after restart. |
| Automatic write-back and echo suppression | PASS | Moving the Kandev task wrote the mapped status to Redmine; after another overlap poll the task remained in the originating step. |
| Watcher lifecycle | PASS | Immediate and background-capable watch polling created one linked task, a second poll deduplicated it, `maxInflightTasks: 1` held, and watch deletion cascade-deleted the owned task. |
| Screenshots | PASS | `docs/screenshots/` contains the connected/redacted settings page plus project, mapping, and watcher cards captured by the live Playwright run. |

Composer submit-time authorization, retry/backoff timing, non-admin custom-field
fallback, redirect stripping, rollback/compensation branches, and background
loop shutdown are covered by focused Go tests because reproducing those fault
conditions through a browser would be slower and less deterministic than the
real boundary tests.

## Repeatable commands

From the plugin checkout with the Kandev checkout available at the `go.mod`
sibling path:

```sh
npm ci --include=dev
go test ./... -race
go vet ./...
npm run test:ui-contract
make verify-package

KANDEV_PLUGIN_E2E_URL=http://127.0.0.1:13081 \
  npm run e2e

KANDEV_PLUGIN_E2E_URL=http://127.0.0.1:13081 \
KANDEV_REDMINE_E2E_BASE_URL=http://127.0.0.1:13080 \
KANDEV_REDMINE_E2E_API_KEY='<first disposable key>' \
KANDEV_REDMINE_E2E_ROTATED_API_KEY='<second disposable key>' \
  npm run e2e:live
```

The candidate Kandev and Redmine services remain available for the author's
final acceptance test. No tag or corrected release is published before that
explicit acceptance.
