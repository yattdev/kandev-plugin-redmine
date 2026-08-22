import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const bundle = await readFile(new URL("./bundle.js", import.meta.url), "utf8");

async function loadBundleTestHooks() {
  globalThis.window = { registerKandevPlugin() {} };
  const source = `${bundle}\nglobalThis.__redmineBundleTestHooks = { createSyncSaveController, syncControllerForWorkspace, workspaceActionInvoke, taskActionInvoke };`;
  await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
  return globalThis.__redmineBundleTestHooks;
}

test("mapping selects use host controls and explicitly added live statuses", () => {
  assert.match(bundle, /const unmappedValue = "__unmapped__"/);
  assert.match(bundle, /value === unmappedValue \? "" : value/);
  assert.match(bundle, /task_priority: priorityMap\[p\.id\] \|\| ""/);
  assert.match(bundle, /redmine-status-step-\$\{status\.id\}/);
  assert.match(bundle, /redmine-status-add-select/);
  assert.match(bundle, /redmine-status-add/);
  assert.match(bundle, /redmine-status-remove-\$\{status\.id\}/);
  assert.match(bundle, /Choose a workflow step for every added Redmine status/);
  assert.match(bundle, /redmine-priority-map-" \+ priority\.id/);
});

test("integration registration supplies a branded icon and workspace enable action", () => {
  assert.match(bundle, /icon: redmineIcon/);
  assert.match(bundle, /action: makeIntegrationEnabledAction\(host\)/);
  assert.match(bundle, /host\.ui\.IntegrationEnabledControl/);
  assert.match(bundle, /"integration.enabled.get"/);
  assert.match(bundle, /"integration.enabled.save"/);
});

test("watcher filters use Kandev Select controls and nonempty optional sentinels", () => {
  assert.match(bundle, /redmine-watch-project/);
  assert.match(bundle, /__any_tracker__/);
  assert.match(bundle, /__any_status__/);
  assert.doesNotMatch(bundle, /h\("select", \{ id: "redmine-new-watch-/);
});

test("project selection uses a dropdown with removable selected projects", () => {
  assert.match(bundle, /redmine-project-select/);
  assert.match(bundle, /redmine-selected-projects/);
  assert.match(bundle, /redmine-project-remove-\$\{id\}/);
  assert.match(bundle, /Select project/);
  assert.doesNotMatch(bundle, /h\(Checkbox, \{[\s\S]*redmine-project-/);
});

test("mapping renders custom field identifiers and derived-mode evidence", () => {
  assert.match(bundle, /redmine-custom-fields/);
  assert.match(bundle, /redmine-custom-field-" \+ field\.id/);
  assert.match(bundle, /#\$\{field\.id\} \$\{field\.name\}/);
  assert.match(bundle, /redmine-custom-fields-derived-note/);
});

test("primary task menu actions retain Link and use verified task actions", () => {
  assert.match(bundle, /registry\.registerTaskAction\(makeLinkTaskAction\(host\)\)/);
  assert.match(bundle, /registry\.registerTaskMenuAction\(makeSetRedmineStatusAction\(host\)\)/);
  assert.match(bundle, /registry\.registerTaskMenuAction\(makeUnlinkRedmineAction\(host\)\)/);
  assert.match(bundle, /group: "primary"/);
  for (const key of ["link.get", "fieldmapping.get", "link.set_status", "link.unset"]) assert.match(bundle, new RegExp(`"${key}"`));
  for (const id of ["redmine-status-modal", "redmine-status-picker", "redmine-status-confirm", "redmine-unlink-modal", "redmine-unlink-confirm"]) assert.match(bundle, new RegExp(id));
});

test("manual status uses workspace scope for field mappings and task scope for links", async () => {
  let registration;
  globalThis.window = {
    registerKandevPlugin(_id, value) {
      registration = value;
    },
  };
  await import(new URL(`./bundle.js?manual-status-contract=${Date.now()}`, import.meta.url));

  const menuActions = [];
  const calls = [];
  const host = {
    jsx: () => null,
    React: {},
    ui: {},
    toast: { success() {}, error() {} },
    openModal() { return { close() {} }; },
    api: {
      async invokeAction(key, context) {
        calls.push({ key, context });
        if (key === "link.get") return { linked: true };
        if (key === "fieldmapping.get") return { live_statuses: [] };
        throw new Error(`unexpected action ${key}`);
      },
    },
  };
  registration.initialize({ registerTaskAction() {}, registerTaskMenuAction(action) { menuActions.push(action); }, registerIntegrationSettings() {} }, host);
  const action = menuActions.find((candidate) => candidate.id === "redmine-set-status");
  await action.run({ workspaceId: "workspace-1", taskId: "task-1" });

  assert.deepEqual(calls, [
    { key: "link.get", context: { workspaceId: "workspace-1", taskId: "task-1", body: {} } },
    { key: "fieldmapping.get", context: { workspaceId: "workspace-1", body: {} } },
  ]);
});

test("sync saves roll back and toast without an unhandled rejection", async () => {
  const { createSyncSaveController } = await loadBundleTestHooks();
  const applied = [];
  const toasts = [];
  const controller = createSyncSaveController(
    async () => { throw new Error("save rejected"); },
    "workspace-1",
    { error(message) { toasts.push(message); } },
    (next) => applied.push(next),
    () => {},
  );
  controller.setOptions({ autoStatusWriteback: false, syncTitleDescription: true });
  assert.equal(await controller.update("autoStatusWriteback", true), false);
  assert.deepEqual(applied, [
    { autoStatusWriteback: true, syncTitleDescription: true },
    { autoStatusWriteback: false, syncTitleDescription: true },
  ]);
  assert.deepEqual(toasts, ["save rejected"]);
});

test("sync saves are serialized and use workspace-only action context", async () => {
  const { createSyncSaveController, workspaceActionInvoke } = await loadBundleTestHooks();
  let release;
  const pending = new Promise((resolve) => { release = resolve; });
  const calls = [];
  const controller = createSyncSaveController(
    async (key, workspaceId, body) => { calls.push({ key, workspaceId, body }); await pending; },
    "workspace-1",
    { error() {} },
    () => {},
    () => {},
  );
  const first = controller.update("autoStatusWriteback", true);
  assert.equal(await controller.update("syncTitleDescription", true), false);
  release();
  assert.equal(await first, true);
  assert.deepEqual(calls, [{ key: "syncoptions.save", workspaceId: "workspace-1", body: { auto_status_writeback: true, sync_title_description: false } }]);

  const contexts = [];
  await workspaceActionInvoke({ api: { invokeAction: async (_key, context) => { contexts.push(context); return {}; } } }, "projects.save", { workspaceId: "workspace-1", taskId: "must-not-send" }, {});
  assert.deepEqual(contexts, [{ workspaceId: "workspace-1", body: {} }]);
});

test("sync controller is recreated when the settings workspace changes", async () => {
  const { syncControllerForWorkspace } = await loadBundleTestHooks();
  const ref = { current: null };
  const calls = [];
  const make = (workspaceId) => syncControllerForWorkspace(
    ref,
    async (key, targetWorkspaceId, body) => { calls.push({ key, workspaceId: targetWorkspaceId, body }); },
    workspaceId,
    { error() {} },
    () => {},
    () => {},
  );
  const first = make("workspace-1");
  await first.update("autoStatusWriteback", true);
  const second = make("workspace-2");
  await second.update("syncTitleDescription", true);
  assert.notEqual(first, second);
  assert.deepEqual(calls.map((call) => call.workspaceId), ["workspace-1", "workspace-2"]);
});
