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
// Task 08 (docs/plans/redmine-plugin/task-08-settings-ui-native-registrations.md
// in kdlbs/kandev) adds registerIntegrationSettings (connection form, project
// picker, field-mapping table, sync-option toggles, watcher management) and
// the reference_sources composer-mention UI on top of this file.

// ---------------------------------------------------------------------------
// Task linking — registerTaskAction({placement:"link"}) opens the shared
// host Link dialog. The plugin supplies only copy strings and an
// onSubmit(reference, signal) callback; host.openTaskLinkDialog is a single
// free-text input (an issue ID, "#123" shorthand, or issue URL), not a
// search/picker UI. Resolving that reference to a real issue and persisting
// the link happen entirely backend-side, via the link.set action
// (server/actions_link.go's parseIssueReference + GetIssue call).
// ---------------------------------------------------------------------------
function makeLinkTaskAction(host) {
  return {
    id: "redmine-link",
    label: "Redmine Issue",
    placement: "link",
    singleTaskOnly: true,
    async run(context) {
      host.openTaskLinkDialog({
        title: "Link Redmine issue",
        description: "Enter a Redmine issue ID, \"#123\", or an issue URL.",
        inputLabel: "Issue",
        placeholder: "#123 or https://redmine.example.com/issues/123",
        emptyError: "Enter an issue ID or URL.",
        failureMessage: "Could not link that Redmine issue. Check the ID and try again.",
        successMessage: "Linked to Redmine issue.",
        inputTestId: "redmine-link-input",
        errorTestId: "redmine-link-error",
        submitTestId: "redmine-link-submit",
        async onSubmit(reference, signal) {
          await host.api.invokeAction(
            "link.set",
            {
              workspaceId: context.workspaceId,
              taskId: context.taskId,
              body: { reference },
            },
            { signal },
          );
        },
      });
    },
  };
}

// ---------------------------------------------------------------------------
// Registration. Keep only what this plugin uses.
// ---------------------------------------------------------------------------
window.registerKandevPlugin("kandev-plugin-redmine", {
  initialize(registry, host) {
    registry.registerTaskAction(makeLinkTaskAction(host));
  },

  destroy() {
    // The host bulk-unregisters everything under this plugin's id; no
    // module-level state to reset yet (task 08 introduces some for the
    // settings page's live subscriptions).
  },
});
