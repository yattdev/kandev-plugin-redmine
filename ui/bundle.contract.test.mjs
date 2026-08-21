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
