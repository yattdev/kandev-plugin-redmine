import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const bundle = await readFile(new URL("./bundle.js", import.meta.url), "utf8");

test("mapping selects use a nonempty unmapped sentinel and save empty mappings", () => {
  assert.match(bundle, /const unmappedValue = "__unmapped__"/);
  assert.match(bundle, /value === unmappedValue \? "" : value/);
  assert.match(bundle, /task_priority: priorityMap\[p\.id\] \|\| ""/);
  assert.match(bundle, /redmine-status-step-\$\{status\.id\}/);
  assert.match(bundle, /redmine-priority-map-" \+ priority\.id/);
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
