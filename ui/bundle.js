// kandev plugin UI bundle — the frontend half of kandev-plugin-redmine.
//
// This is a hand-written, NO-BUILD plain-JS ES module. It ships byte-for-byte
// inside the package tar.gz under ui/bundle.js, and kandev serves it directly
// from the extracted package at GET /api/plugins/<id>/ui/bundle.js, then
// dynamically imports it as a native ES module. There is nothing to build:
// edit this file and repackage (`make package` / `make package-host`).
//
// The contract, in three touch points:
//   - window.registerKandevPlugin(id, { initialize, destroy }) is the single
//     global entry point the host calls once this module has been evaluated.
//     `id` MUST match manifest.yaml's id.
//   - The `host` object handed to initialize() carries the SHARED host React
//     instance (`host.React`, `host.jsx` == host.React.createElement) plus a
//     curated design system (`host.ui`), imperative toasts (`host.toast`),
//     shared helpers (`host.utils`), navigation (`host.navigate`), the task
//     Link dialog (`host.openTaskLinkDialog`), and authenticated backend
//     calls (`host.api.invokeAction`). NEVER import or bundle your own React
//     — that breaks hook identity across the host tree. The same goes for
//     recharts: use the `host.ui.Chart*` wrappers.
//   - `registry` is where nav items, routes, integration settings pages, task
//     actions, and slot components are declared. Every registration is
//     tracked under this plugin's id, so the host bulk-unregisters everything
//     when the plugin is disabled.
//
// This file starts as a bare skeleton; task 08
// (docs/plans/redmine-plugin/task-08-settings-ui-native-registrations.md in
// kdlbs/kandev) fills in registerIntegrationSettings (connection form,
// project picker, field-mapping table, sync-option toggles, watcher
// management) and registerTaskAction({placement:"link"}).

window.registerKandevPlugin("kandev-plugin-redmine", {
  initialize(registry, host) {
    // Populated by task 08.
  },

  destroy() {
    // The host bulk-unregisters everything under this plugin's id; reset any
    // module-level state here once task 08 introduces some.
  },
});
