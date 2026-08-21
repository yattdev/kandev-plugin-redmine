# Disposable integration run — 2026-08-21

This is a task-owned, non-production verification run. It is evidence for the
packaged-plugin installation and connection paths; it is not author acceptance
and does not replace the scenario coverage required by [the canonical
specification](spec.md).

## Environment

- Plugin checkout: commit `94c7f69`; package
  `kandev-plugin-redmine-0.1.0.tar.gz`, built with `make verify-package-host`.
- Kandev host: local compatible source checkout at `b0feb95dd`, launched with
  `go run ./cmd/kandev __backend --port 13001`.
- Kandev state: `/tmp/redmine-kandev-home`; all data is disposable.
- Redmine: official `redmine:6.0` Docker image, no volume, published as
  `0.0.0.0:13000` and reachable at `http://192.168.50.131:13000`.
- Two Kandev workspaces were created solely for this run.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| Package validation | PASS | `make verify-package-host` generated an archive with validated manifest, executable, UI bundle, and checksums. |
| Plugin installation and spawn | PASS | `POST /api/plugins/install` accepted the archive; `GET /api/plugins` reported `status: active`, zero restarts, all manifest actions, reference source, and UI registration. |
| Disable/re-enable | PASS | `POST /api/plugins/kandev-plugin-redmine/disable`, then `/enable`, returned an active plugin. |
| Workspace action routing | PASS | `connection.get` returned `{"state":"disconnected"}` independently for both test workspaces. |
| Redmine REST-disabled classification | PASS | A live `connection.save` returned the plugin-owned `api_disabled` classification and the workspace remained disconnected. |
| Valid live connection | PASS | After enabling Redmine's REST API and provisioning a disposable admin API token, `connection.save` returned `connected` and `last_ok`. The API key was not returned. |
| Cross-workspace isolation | PASS | The connected workspace returned its base URL and health timestamp; the second workspace remained `disconnected`. |

## Commands

```sh
GOWORK=/tmp/redmine-plugin-go.work \
  KANDEV_SDK=/path/to/kandev/apps/backend make verify-package-host

KANDEV_HOME_DIR=/tmp/redmine-kandev-home \
  KANDEV_DATABASE_PATH=/tmp/redmine-kandev-home/data/kandev.db \
  go run ./cmd/kandev __backend --port 13001

docker run -d --rm --name redmine-plugin-integration -p 13000:3000 redmine:6.0
curl -F package=@kandev-plugin-redmine-0.1.0.tar.gz \
  http://127.0.0.1:13001/api/plugins/install
```

The run intentionally does not record the disposable API token. Destroy the
container and `/tmp/redmine-kandev-home` after review; neither contains
production data.

## Still required before a corrected release

Exercise issue creation and attachment upload, project and field mappings,
task link/unlink, inbound closed-status sync, cursor restart, title/description
sync, manual and automatic write-back with echo suppression, watcher
deduplication/throttling, composer authorization, outage/reconnect, and
connection/watch cleanup. Then obtain the author's explicit acceptance test
before publishing a non-preview release.
