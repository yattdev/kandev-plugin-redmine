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
// Composer `#` mentions (reference_sources in manifest.yaml) have no
// frontend registration call — they are purely manifest-declared plus the
// backend SearchEntityReferences/AuthorizeEntityReference implementation
// (server/references.go). There is nothing to add here for that surface.

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

function actionInvoke(host, key, actionContext, body) {
  return host.api.invokeAction(key, { ...actionContext, body }).then((result) => {
    if (result && result.kind) throw new Error(result.error || "Redmine action failed.");
    return result;
  });
}

function workspaceActionInvoke(host, key, context, body) {
  return actionInvoke(host, key, { workspaceId: context.workspaceId }, body);
}

function taskActionInvoke(host, key, context, body) {
  return actionInvoke(host, key, { workspaceId: context.workspaceId, taskId: context.taskId }, body);
}

function makeRedmineIcon(host) {
  return function RedmineIcon({ className }) {
    return host.jsx(
      "svg",
      { className, viewBox: "0 0 24 24", "aria-hidden": true, focusable: false, fill: "currentColor" },
      host.jsx("path", { d: "M4 3h9a6 6 0 0 1 2.1 11.62L21 21h-5.45l-5.05-6H9v6H4V3Zm5 4v4h4a2 2 0 1 0 0-4H9Z" }),
    );
  };
}

function makeIntegrationEnabledAction(host) {
  return function RedmineIntegrationEnabledAction({ workspaceId }) {
    const React = host.React;
    const [enabled, setEnabled] = React.useState(null);
    React.useEffect(() => {
      let active = true;
      if (!workspaceId) { setEnabled(null); return () => { active = false; }; }
      workspaceActionInvoke(host, "integration.enabled.get", { workspaceId }, {}).then((result) => {
        if (active) setEnabled(result.enabled !== false);
      }).catch((err) => host.toast.error(err.message || String(err)));
      return () => { active = false; };
    }, [workspaceId]);
    if (!workspaceId || enabled === null) return null;
    return host.jsx(host.ui.IntegrationEnabledControl, {
      id: "redmine",
      name: "Redmine",
      enabled,
      persist: async (nextEnabled) => {
        await workspaceActionInvoke(host, "integration.enabled.save", { workspaceId }, { enabled: nextEnabled });
        setEnabled(nextEnabled);
      },
    });
  };
}

// The controller serializes option writes. Keeping this outside the React
// component makes its failure rollback behavior testable without a browser.
function createSyncSaveController(invoke, workspaceId, toast, apply, setSaving) {
  let options = { autoStatusWriteback: false, syncTitleDescription: false };
  let pending = false;
  return {
    setOptions(next) { options = next; },
    async update(key, value) {
      if (pending) return false;
      const prior = options;
      const next = { ...prior, [key]: value };
      pending = true;
      setSaving(true);
      options = next;
      apply(next);
      try {
        await invoke("syncoptions.save", workspaceId, {
          auto_status_writeback: next.autoStatusWriteback,
          sync_title_description: next.syncTitleDescription,
        });
        return true;
      } catch (err) {
        options = prior;
        apply(prior);
        toast.error(err && err.message ? err.message : String(err));
        return false;
      } finally {
        pending = false;
        setSaving(false);
      }
    },
  };
}

function syncControllerForWorkspace(ref, invoke, workspaceId, toast, apply, setSaving) {
  if (!ref.current || ref.current.workspaceId !== workspaceId) {
    ref.current = { workspaceId, controller: createSyncSaveController(invoke, workspaceId, toast, apply, setSaving) };
  }
  return ref.current.controller;
}

function makeSetRedmineStatusAction(host) {
  const h = host.jsx;
  return {
    id: "redmine-set-status", label: "Set Redmine status", group: "primary", singleTaskOnly: true,
    async run(context) {
      try {
        const link = await taskActionInvoke(host, "link.get", context, {});
        if (!link.linked) throw new Error("Link this task to a Redmine issue first.");
        const fields = await workspaceActionInvoke(host, "fieldmapping.get", context, {});
        const statuses = fields.live_statuses || [];
        const closeRef = {};
        function StatusModal() {
          const React = host.React;
          const [statusId, setStatusId] = React.useState("");
          const [error, setError] = React.useState(null);
          const submit = async () => {
            const id = Number(statusId);
            if (!Number.isSafeInteger(id) || id <= 0) { setError("Select a Redmine status."); return; }
            try { await taskActionInvoke(host, "link.set_status", context, { status_id: id }); host.toast.success("Redmine status updated."); closeRef.close && closeRef.close(); }
            catch (err) { setError(err.message || String(err)); }
          };
          return h("div", { "data-testid": "redmine-status-modal" },
            h("label", { htmlFor: "redmine-status-picker" }, "Redmine status"),
            h("select", { id: "redmine-status-picker", "data-testid": "redmine-status-picker", value: statusId, onChange: (event) => setStatusId(event.target.value) }, [h("option", { value: "" }, "Select status")].concat(statuses.map((status) => h("option", { key: status.id, value: status.id }, status.name)))),
            error ? h("p", { "data-testid": "redmine-status-error" }, error) : null,
            h("button", { type: "button", "data-testid": "redmine-status-confirm", onClick: submit }, "Update status"),
          );
        }
        const handle = host.openModal({ title: "Set Redmine status", size: "sm", content: StatusModal });
        closeRef.close = handle.close;
      } catch (err) { host.toast.error(err.message || String(err)); }
    },
  };
}

function makeUnlinkRedmineAction(host) {
  const h = host.jsx;
  return {
    id: "redmine-unlink", label: "Unlink Redmine issue", group: "primary", singleTaskOnly: true,
    async run(context) {
      try {
        const link = await taskActionInvoke(host, "link.get", context, {});
        if (!link.linked) throw new Error("This task is not linked to a Redmine issue.");
        const closeRef = {};
        function UnlinkModal() {
          const [error, setError] = host.React.useState(null);
          const confirm = async () => { try { await taskActionInvoke(host, "link.unset", context, {}); host.toast.success("Redmine issue unlinked."); closeRef.close && closeRef.close(); } catch (err) { setError(err.message || String(err)); } };
          return h("div", { "data-testid": "redmine-unlink-modal" }, h("p", null, "Unlink this task from Redmine?"), error ? h("p", { "data-testid": "redmine-unlink-error" }, error) : null, h("button", { type: "button", "data-testid": "redmine-unlink-confirm", onClick: confirm }, "Unlink"));
        }
        const handle = host.openModal({ title: "Unlink Redmine issue", size: "sm", content: UnlinkModal });
        closeRef.close = handle.close;
      } catch (err) { host.toast.error(err.message || String(err)); }
    },
  };
}

// ---------------------------------------------------------------------------
// Settings page — registerIntegrationSettings. Built from independent
// sections (Connection, Projects, Field mapping, Sync options, Watchers) so
// each can be read/edited without unpicking the others. Every section calls
// host.api.invokeAction directly against the actions declared in
// manifest.yaml; there is no client-side caching layer, so a save always
// re-fetches to reflect exactly what the backend now has stored.
// ---------------------------------------------------------------------------
function makeSettingsComponent(host) {
  const { jsx: h, ui, toast } = host;
  const {
    Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter,
    Button, Input, Label, Badge, Switch,
    Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
    Table, TableHeader, TableBody, TableRow, TableHead, TableCell,
    Empty, EmptyHeader, EmptyTitle, EmptyDescription,
    Spinner,
  } = ui;

  async function invoke(key, workspaceId, body) {
    const result = await host.api.invokeAction(key, { workspaceId, body });
    if (result && result.kind) {
      throw new Error(result.error || "Redmine action failed.");
    }
    return result;
  }

  function errorMessage(err) {
    return err && err.message ? err.message : String(err);
  }

  // -- Connection ------------------------------------------------------
  function ConnectionSection({ workspaceId, connection, reload }) {
    const React = host.React;
    const [baseUrl, setBaseUrl] = React.useState(connection.base_url || "");
    const [apiKey, setApiKey] = React.useState("");
    const [saving, setSaving] = React.useState(false);
    const [error, setError] = React.useState(null);

    const onSave = async () => {
      setSaving(true);
      setError(null);
      try {
        const result = await invoke("connection.save", workspaceId, { base_url: baseUrl, api_key: apiKey });
        if (result && result.kind) {
          setError(result.error || "Could not connect to Redmine.");
          return;
        }
        toast.success("Connected to Redmine.");
        setApiKey("");
        await reload();
      } catch (err) {
        setError(errorMessage(err));
      } finally {
        setSaving(false);
      }
    };

    const onDisconnect = async () => {
      setSaving(true);
      try {
        await invoke("connection.disconnect", workspaceId, {});
        toast.success("Disconnected from Redmine.");
        await reload();
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setSaving(false);
      }
    };

    const badgeVariant =
      connection.state === "connected" ? "default" : connection.state === "degraded" ? "destructive" : "secondary";

    return h(
      Card,
      { id: "redmine-connection-card" },
      h(
        CardHeader,
        null,
        h(CardTitle, null, "Connection"),
        h(CardDescription, null, "Connect a Redmine instance with an API key from your Redmine account's My account page."),
      ),
      h(
        CardContent,
        { className: "space-y-4" },
        h(
          "div",
          { className: "flex items-center gap-2" },
          h(Badge, { variant: badgeVariant, id: "redmine-connection-state" }, connection.state),
          connection.last_error
            ? h("span", { className: "text-destructive text-xs", id: "redmine-connection-last-error" }, connection.last_error)
            : null,
        ),
        h(
          "div",
          { className: "space-y-2" },
          h(Label, { htmlFor: "redmine-base-url" }, "Base URL"),
          h(Input, {
            id: "redmine-base-url",
            "data-testid": "redmine-base-url-input",
            placeholder: "https://redmine.example.com",
            value: baseUrl,
            onChange: (e) => setBaseUrl(e.target.value),
          }),
        ),
        h(
          "div",
          { className: "space-y-2" },
          h(Label, { htmlFor: "redmine-api-key" }, "API key"),
          h(Input, {
            id: "redmine-api-key",
            "data-testid": "redmine-api-key-input",
            type: "password",
            placeholder: connection.state === "connected" ? "Leave blank to keep the current key" : "",
            value: apiKey,
            onChange: (e) => setApiKey(e.target.value),
          }),
        ),
        error ? h("p", { className: "text-destructive text-sm", id: "redmine-connection-error" }, error) : null,
      ),
      h(
        CardFooter,
        { className: "gap-2" },
        h(
          Button,
          { id: "redmine-connection-save", "data-testid": "redmine-connection-save", disabled: saving || !baseUrl || (!apiKey && connection.state !== "connected"), onClick: onSave },
          "Save",
        ),
        connection.state !== "disconnected"
          ? h(Button, { variant: "outline", disabled: saving, onClick: onDisconnect }, "Disconnect")
          : null,
      ),
    );
  }

  // -- Projects ----------------------------------------------------------
  function ProjectsSection({ workspaceId, connected }) {
    const React = host.React;
    const [loading, setLoading] = React.useState(true);
    const [projects, setProjects] = React.useState([]);
    const [selected, setSelected] = React.useState(new Set());
    const [projectToAdd, setProjectToAdd] = React.useState("");
    const [saving, setSaving] = React.useState(false);

    const load = React.useCallback(async () => {
      if (!connected) {
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const result = await invoke("projects.list", workspaceId, {});
        setProjects((result && result.projects) || []);
        setSelected(new Set((result && result.selected_ids) || []));
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setLoading(false);
      }
    }, [workspaceId, connected]);

    React.useEffect(() => {
      load();
    }, [load]);

    if (!connected) return null;
    if (loading) return h(Spinner, { id: "redmine-projects-loading" });

    const addProject = (value) => {
      if (value === "__select_project__") {
        setProjectToAdd("");
        return;
      }
      const id = Number(value);
      if (!Number.isSafeInteger(id) || id <= 0) return;
      const next = new Set(selected);
      next.add(id);
      setSelected(next);
      setProjectToAdd("");
    };

    const removeProject = (id) => {
      const next = new Set(selected);
      next.delete(id);
      setSelected(next);
    };

    const onSave = async () => {
      if (saving) return;
      setSaving(true);
      try {
        await invoke("projects.save", workspaceId, { project_ids: Array.from(selected) });
        toast.success("Project selection saved.");
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setSaving(false);
      }
    };

    return h(
      Card,
      { id: "redmine-projects-card" },
      h(CardHeader, null, h(CardTitle, null, "Projects"), h(CardDescription, null, "Select which Redmine projects sync.")),
      h(
        CardContent,
        null,
        projects.length === 0
          ? h(Empty, { id: "redmine-projects-empty" }, h(EmptyHeader, null, h(EmptyTitle, null, "No projects found")))
          : h(
              "div",
              { className: "space-y-3" },
              h(
                "div",
                { className: "space-y-2" },
                h(Label, { htmlFor: "redmine-project-select" }, "Redmine project"),
                h(
                  Select,
                  { value: projectToAdd || "__select_project__", onValueChange: addProject },
                  h(SelectTrigger, { id: "redmine-project-select", "data-testid": "redmine-project-select", className: "w-full" }, h(SelectValue, { placeholder: "Select project" })),
                  h(SelectContent, null, [
                    h(SelectItem, { key: "__select_project__", value: "__select_project__" }, "Select project"),
                  ].concat(projects.map((project) => h(SelectItem, { key: project.id, value: String(project.id), disabled: selected.has(project.id) }, project.name)))),
                ),
              ),
              selected.size === 0
                ? h("p", { className: "text-muted-foreground text-sm", "data-testid": "redmine-projects-none" }, "No projects selected.")
                : h(
                    "ul",
                    { className: "space-y-2", "data-testid": "redmine-selected-projects" },
                    Array.from(selected).map((id) => {
                      const project = projects.find((candidate) => candidate.id === id);
                      return h(
                        "li",
                        { key: id, className: "flex items-center justify-between gap-2" },
                        h("span", null, project ? project.name : id),
                        h(Button, { type: "button", variant: "ghost", size: "sm", "data-testid": `redmine-project-remove-${id}`, onClick: () => removeProject(id) }, "Remove"),
                      );
                    }),
                  ),
            ),
      ),
      h(CardFooter, null, h(Button, { id: "redmine-projects-save", "data-testid": "redmine-projects-save", disabled: saving, onClick: onSave }, saving ? "Saving…" : "Save projects")),
    );
  }

  // -- Field mapping -------------------------------------------------------
  function FieldMappingSection({ workspaceId, connected }) {
    const React = host.React;
    const unmappedValue = "__unmapped__";
    const [loading, setLoading] = React.useState(true);
    const [live, setLive] = React.useState(null);
    const [workflows, setWorkflows] = React.useState([]);
    const [workflowId, setWorkflowId] = React.useState("");
    const [statusIDs, setStatusIDs] = React.useState([]);
    const [statusToAdd, setStatusToAdd] = React.useState("");
    const [statusSteps, setStatusSteps] = React.useState({});
    const [trackerLabels, setTrackerLabels] = React.useState({});
    const [priorityMap, setPriorityMap] = React.useState({});
    const [saving, setSaving] = React.useState(false);

    const load = React.useCallback(async () => {
      if (!connected) {
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const [fields, wf] = await Promise.all([
          invoke("fieldmapping.get", workspaceId, {}),
          invoke("workflows.list", workspaceId, {}),
        ]);
        setLive(fields);
        const availableWorkflows = (wf && wf.workflows) || [];
        setWorkflows(availableWorkflows);
        setWorkflowId(fields.workflow_id || (availableWorkflows[0] && availableWorkflows[0].id) || "");

        const steps = {};
        (fields.statuses || []).forEach((s) => {
          steps[s.redmine_status_id] = s.workflow_step_id;
        });
        setStatusSteps(steps);
        setStatusIDs((fields.statuses || []).filter((status) => status.workflow_step_id).map((status) => status.redmine_status_id));

        const labels = {};
        (fields.trackers || []).forEach((t) => {
          labels[t.redmine_tracker_id] = t.task_label;
        });
        setTrackerLabels(labels);

        const priorities = {};
        (fields.priorities || []).forEach((p) => {
          priorities[p.redmine_priority_id] = p.task_priority;
        });
        setPriorityMap(priorities);
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setLoading(false);
      }
    }, [workspaceId, connected]);

    React.useEffect(() => {
      load();
    }, [load]);

    if (!connected) return null;
    if (loading || !live) return h(Spinner, { id: "redmine-fieldmapping-loading" });

    const selectedWorkflow = workflows.find((wf) => wf.id === workflowId);
    const allSteps = (selectedWorkflow && selectedWorkflow.steps) || [];
    const mappedStatuses = statusIDs.map((id) => (live.live_statuses || []).find((status) => status.id === id)).filter(Boolean);
    const availableStatuses = (live.live_statuses || []).filter((status) => !statusIDs.includes(status.id));

    const addStatus = () => {
      const id = Number(statusToAdd);
      if (!Number.isSafeInteger(id) || id <= 0 || statusIDs.includes(id)) return;
      setStatusIDs([...statusIDs, id]);
      setStatusToAdd("");
    };

    const removeStatus = (id) => {
      setStatusIDs(statusIDs.filter((statusID) => statusID !== id));
      setStatusSteps((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
    };

    const onSave = async () => {
      if (saving) return;
      if (statusIDs.some((id) => !statusSteps[id])) {
        toast.error("Choose a workflow step for every added Redmine status.");
        return;
      }
      const statuses = mappedStatuses.map((s) => ({
        redmine_status_id: s.id,
        redmine_name: s.name,
        is_closed: s.is_closed,
        workflow_step_id: statusSteps[s.id] || "",
      }));
      const trackers = (live.live_trackers || []).map((t) => ({
        redmine_tracker_id: t.id,
        redmine_name: t.name,
        task_label: trackerLabels[t.id] || "",
      }));
      const priorities = (live.live_priorities || []).map((p) => ({
        redmine_priority_id: p.id,
        redmine_name: p.name,
        task_priority: priorityMap[p.id] || "",
      }));
      setSaving(true);
      try {
        await invoke("fieldmapping.save", workspaceId, { workflow_id: workflowId, statuses, trackers, priorities });
        toast.success("Field mapping saved.");
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setSaving(false);
      }
    };

    return h(
      Card,
      { id: "redmine-fieldmapping-card" },
      h(
        CardHeader,
        null,
        h(CardTitle, null, "Field mapping"),
        h(CardDescription, null, "Map live Redmine statuses, trackers, and priorities to Kandev — nothing here is hardcoded."),
      ),
      h(
        CardContent,
        { className: "space-y-6" },
        h("div", { className: "space-y-2" },
          h(Label, { htmlFor: "redmine-mapping-workflow" }, "Kandev workflow"),
          h(Select, { value: workflowId, onValueChange: (nextWorkflowId) => {
            const nextWorkflow = workflows.find((workflow) => workflow.id === nextWorkflowId);
            const allowedStepIDs = new Set(((nextWorkflow && nextWorkflow.steps) || []).map((step) => step.id));
            setWorkflowId(nextWorkflowId);
            setStatusSteps((current) => Object.fromEntries(Object.entries(current).map(([statusID, stepID]) => [statusID, allowedStepIDs.has(stepID) ? stepID : ""])));
          } },
            h(SelectTrigger, { id: "redmine-mapping-workflow", "data-testid": "redmine-mapping-workflow", className: "w-full" }, h(SelectValue, { placeholder: "Select workflow" })),
            h(SelectContent, null, workflows.map((workflow) => h(SelectItem, { key: workflow.id, value: workflow.id }, workflow.name))),
          ),
        ),
        h(
          "div",
          { className: "space-y-3" },
          h("h4", { className: "mb-2 text-sm font-medium" }, "Statuses → workflow step"),
          mappedStatuses.length ? h(
            Table,
            { id: "redmine-status-mapping-table" },
            h(TableHeader, null, h(TableRow, null, h(TableHead, null, "Redmine status"), h(TableHead, null, "Workflow step"), h(TableHead, { className: "w-20" }, ""))),
            h(
              TableBody,
              null,
              mappedStatuses.map((status) =>
                h(
                  TableRow,
                  { key: status.id },
                  h(TableCell, null, status.name, status.is_closed ? h(Badge, { variant: "secondary", className: "ml-2" }, "closed") : null),
                  h(
                    TableCell,
                    null,
                    h(
                      Select,
                      {
                        value: statusSteps[status.id] || unmappedValue,
                        onValueChange: (value) => setStatusSteps({ ...statusSteps, [status.id]: value === unmappedValue ? "" : value }),
                      },
                      h(SelectTrigger, { "data-testid": `redmine-status-step-${status.id}` }, h(SelectValue, { placeholder: "Unmapped" })),
                      h(
                        SelectContent,
                        null,
                        [h(SelectItem, { key: unmappedValue, value: unmappedValue }, "Unmapped")].concat(allSteps.map((step) => h(SelectItem, { key: step.id, value: step.id }, step.name))),
                      ),
                    ),
                  ),
                  h(TableCell, null, h(Button, { type: "button", variant: "ghost", size: "sm", "data-testid": `redmine-status-remove-${status.id}`, onClick: () => removeStatus(status.id) }, "Remove")),
                ),
              ),
            ),
          ) : h("p", { className: "text-muted-foreground text-sm", "data-testid": "redmine-status-mapping-empty" }, "No Redmine statuses mapped yet."),
          h("div", { className: "flex items-end gap-2" },
            h("div", { className: "min-w-0 flex-1 space-y-2" },
              h(Label, null, "Add Redmine status"),
              h(Select, { value: statusToAdd || "__select_status__", onValueChange: (value) => setStatusToAdd(value === "__select_status__" ? "" : value) },
                h(SelectTrigger, { "data-testid": "redmine-status-add-select", className: "w-full" }, h(SelectValue, { placeholder: "Select a live Redmine status" })),
                h(SelectContent, null, [h(SelectItem, { key: "__select_status__", value: "__select_status__" }, "Select a live Redmine status")].concat(availableStatuses.map((status) => h(SelectItem, { key: status.id, value: String(status.id) }, status.name)))),
              ),
            ),
            h(Button, { type: "button", variant: "outline", "data-testid": "redmine-status-add", disabled: !statusToAdd, onClick: addStatus }, "Add status"),
          ),
        ),
        h(
          "div",
          null,
          h("h4", { className: "mb-2 text-sm font-medium" }, "Trackers → task label"),
          h(
            "div",
            { className: "space-y-2" },
            (live.live_trackers || []).map((tracker) =>
              h(
                "div",
                { key: tracker.id, className: "flex items-center gap-2" },
                h("span", { className: "w-32 text-sm" }, tracker.name),
                h(Input, {
                  "data-testid": "redmine-tracker-label-" + tracker.id,
                  value: trackerLabels[tracker.id] || "",
                  onChange: (e) => setTrackerLabels({ ...trackerLabels, [tracker.id]: e.target.value }),
                  placeholder: "label",
                }),
              ),
            ),
          ),
        ),
        h(
          "div",
          null,
          h("h4", { className: "mb-2 text-sm font-medium" }, "Priorities → task priority"),
          h(
            "div",
            { className: "space-y-2" },
            (live.live_priorities || []).map((priority) =>
              h(
                "div",
                { key: priority.id, className: "flex items-center gap-2" },
                h("span", { className: "w-32 text-sm" }, priority.name),
                h(
                  Select,
                  {
                    "data-testid": "redmine-priority-map-" + priority.id,
                    value: priorityMap[priority.id] || unmappedValue,
                    onValueChange: (value) => setPriorityMap({ ...priorityMap, [priority.id]: value === unmappedValue ? "" : value }),
                  },
                  h(SelectTrigger, null, h(SelectValue, null)),
                  h(
                    SelectContent,
                    null,
                    [h(SelectItem, { key: unmappedValue, value: unmappedValue }, "Unmapped")].concat(["critical", "high", "medium", "low"].map((p) => h(SelectItem, { key: p, value: p }, p))),
                  ),
                ),
              ),
            ),
          ),
        ),
        h("div", { id: "redmine-custom-fields", "data-testid": "redmine-custom-fields" },
          h("h4", { className: "mb-2 text-sm font-medium" }, "Redmine custom fields"),
          (live.custom_fields || []).length ? h("ul", null, (live.custom_fields || []).map((field) => h("li", { key: field.id, "data-testid": "redmine-custom-field-" + field.id }, `#${field.id} ${field.name}`))) : h("p", { className: "text-muted-foreground text-xs" }, "No custom fields available."),
          live.custom_fields_derived ? h("p", { className: "text-muted-foreground text-xs", id: "redmine-custom-fields-derived-note" }, "Custom fields were derived from recent issues because this API key cannot list them.") : null,
        ),
      ),
      h(CardFooter, null, h(Button, { id: "redmine-fieldmapping-save", "data-testid": "redmine-fieldmapping-save", disabled: saving, onClick: onSave }, saving ? "Saving…" : "Save mapping")),
    );
  }

  // -- Sync options --------------------------------------------------------
  function SyncOptionsSection({ workspaceId, connected }) {
    const React = host.React;
    const [autoStatusWriteback, setAutoStatusWriteback] = React.useState(false);
    const [syncTitleDescription, setSyncTitleDescription] = React.useState(false);
    const [loading, setLoading] = React.useState(true);
    const [saving, setSaving] = React.useState(false);
    const controllerRef = React.useRef(null);
    const controller = syncControllerForWorkspace(
        controllerRef,
        invoke,
        workspaceId,
        toast,
        (next) => {
          setAutoStatusWriteback(next.autoStatusWriteback);
          setSyncTitleDescription(next.syncTitleDescription);
        },
        setSaving,
      );

    React.useEffect(() => {
      if (!connected) { setLoading(false); return; }
      let active = true;
      invoke("syncoptions.get", workspaceId, {}).then((options) => {
        if (!active) return;
        const next = { autoStatusWriteback: Boolean(options.auto_status_writeback), syncTitleDescription: Boolean(options.sync_title_description) };
        controller.setOptions(next);
        setAutoStatusWriteback(next.autoStatusWriteback);
        setSyncTitleDescription(next.syncTitleDescription);
      }).catch((err) => toast.error(errorMessage(err))).finally(() => { if (active) setLoading(false); });
      return () => { active = false; };
    }, [workspaceId, connected]);

    if (!connected) return null;
    if (loading) return h(Spinner, { id: "redmine-syncoptions-loading", "data-testid": "redmine-syncoptions-loading" });

    return h(
      Card,
      { id: "redmine-syncoptions-card" },
      h(CardHeader, null, h(CardTitle, null, "Sync options"), h(CardDescription, null, "Both default off: manual write-back only.")),
      h(
        CardContent,
        { className: "space-y-4" },
        h(
          "div",
          { className: "flex items-center justify-between" },
          h("div", null, h(Label, { htmlFor: "redmine-auto-writeback" }, "Automatic status write-back"), h(
            "p",
            { className: "text-muted-foreground text-xs" },
            "Push a mapped status to Redmine when a linked task moves to that workflow step.",
          )),
          h(Switch, {
            id: "redmine-auto-writeback",
            "data-testid": "redmine-auto-writeback",
            checked: autoStatusWriteback,
            disabled: saving,
            onCheckedChange: (checked) => {
              void controller.update("autoStatusWriteback", checked);
            },
          }),
        ),
        h(
          "div",
          { className: "flex items-center justify-between" },
          h("div", null, h(Label, { htmlFor: "redmine-sync-title" }, "Sync title & description"), h(
            "p",
            { className: "text-muted-foreground text-xs" },
            "Copy the Redmine subject/description onto the linked task on inbound sync.",
          )),
          h(Switch, {
            id: "redmine-sync-title",
            "data-testid": "redmine-sync-title",
            checked: syncTitleDescription,
            disabled: saving,
            onCheckedChange: (checked) => {
              void controller.update("syncTitleDescription", checked);
            },
          }),
        ),
      ),
    );
  }

  // -- Watchers --------------------------------------------------------
  function WatchersSection({ workspaceId, connected }) {
    const React = host.React;
    const [watches, setWatches] = React.useState([]);
    const [loading, setLoading] = React.useState(true);
    const [projects, setProjects] = React.useState([]);
    const [trackers, setTrackers] = React.useState([]);
    const [statuses, setStatuses] = React.useState([]);
    const [filterOptions, setFilterOptions] = React.useState({ priorities: [], assignees: [], categories: [], custom_field_values: {}, custom_fields: [] });
    const [newProjectId, setNewProjectId] = React.useState("");
    const [newTrackerId, setNewTrackerId] = React.useState("");
    const [newStatusId, setNewStatusId] = React.useState("");
    const [newPriorityId, setNewPriorityId] = React.useState("");
    const [newAssigneeId, setNewAssigneeId] = React.useState("");
    const [newCategoryId, setNewCategoryId] = React.useState("");
    const [customFilters, setCustomFilters] = React.useState({});
    const [customFieldToAdd, setCustomFieldToAdd] = React.useState("");
    const [refreshingFilters, setRefreshingFilters] = React.useState(false);
    const [newMaxInflight, setNewMaxInflight] = React.useState("");
    const [creating, setCreating] = React.useState(false);
    const [busyWatchIDs, setBusyWatchIDs] = React.useState(new Set());

    const load = React.useCallback(async () => {
      if (!connected) {
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const [result, projectResult, mappingResult] = await Promise.all([
          invoke("watches.list", workspaceId, {}), invoke("projects.list", workspaceId, {}), invoke("fieldmapping.get", workspaceId, {}),
        ]);
        setWatches((result && result.watches) || []);
        const selected = new Set((projectResult && projectResult.selected_ids) || []);
        setProjects(((projectResult && projectResult.projects) || []).filter((project) => selected.has(project.id)));
        setTrackers((mappingResult && mappingResult.live_trackers) || []);
        setStatuses((mappingResult && mappingResult.live_statuses) || []);
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setLoading(false);
      }
    }, [workspaceId, connected]);

    React.useEffect(() => {
      load();
    }, [load]);

    const refreshFilterOptions = async (projectID = newProjectId) => {
      const id = Number(projectID);
      if (!Number.isSafeInteger(id) || id <= 0) return;
      setRefreshingFilters(true);
      try {
        const options = await invoke("watches.filter_options", workspaceId, { project_id: id });
        setFilterOptions(options || { priorities: [], assignees: [], categories: [], custom_field_values: {}, custom_fields: [] });
      } catch (err) {
        toast.error(errorMessage(err));
      } finally { setRefreshingFilters(false); }
    };

    const selectProject = (value) => {
      const projectID = value === "__select_project__" ? "" : value;
      setNewProjectId(projectID);
      setNewTrackerId(""); setNewStatusId(""); setNewPriorityId(""); setNewAssigneeId(""); setNewCategoryId(""); setCustomFilters({}); setCustomFieldToAdd("");
      setFilterOptions({ priorities: [], assignees: [], categories: [], custom_field_values: {}, custom_fields: [] });
      if (projectID) void refreshFilterOptions(projectID);
    };

    if (!connected) return null;
    if (loading) return h(Spinner, { id: "redmine-watchers-loading" });

    const onCreate = async () => {
      if (creating) return;
      const projectId = Number(newProjectId);
      if (!Number.isSafeInteger(projectId) || projectId <= 0) {
        toast.error("Select a project.");
        return;
      }
      const trackerId = newTrackerId === "" ? null : Number(newTrackerId);
      if (trackerId !== null && (!Number.isSafeInteger(trackerId) || trackerId <= 0)) {
        toast.error("Select a valid tracker.");
        return;
      }
      const statusId = newStatusId === "" ? null : Number(newStatusId);
      if (statusId !== null && (!Number.isSafeInteger(statusId) || statusId <= 0)) {
        toast.error("Select a valid status.");
        return;
      }
      const priorityId = newPriorityId === "" ? null : Number(newPriorityId);
      const assigneeId = newAssigneeId === "" ? null : Number(newAssigneeId);
      const categoryId = newCategoryId === "" ? null : Number(newCategoryId);
      for (const [label, value] of [["priority", priorityId], ["assignee", assigneeId], ["category", categoryId]]) {
        if (value !== null && (!Number.isSafeInteger(value) || value <= 0)) { toast.error(`Select a valid ${label}.`); return; }
      }
      const maxInflight = Number(newMaxInflight);
      if (newMaxInflight !== "" && (!Number.isSafeInteger(maxInflight) || maxInflight < 0)) {
        toast.error("Max inflight tasks must be a non-negative integer.");
        return;
      }
      setCreating(true);
      try {
        await invoke("watches.create", workspaceId, {
          project_id: projectId,
          tracker_id: trackerId,
          status_id: statusId,
          priority_id: priorityId,
          assignee_id: assigneeId,
          category_id: categoryId,
          custom_field_filters: customFilters,
          max_inflight_tasks: newMaxInflight === "" ? 0 : maxInflight,
          enabled: true,
        });
        setNewProjectId("");
        setNewTrackerId("");
        setNewStatusId("");
        setNewPriorityId(""); setNewAssigneeId(""); setNewCategoryId(""); setCustomFilters({}); setCustomFieldToAdd("");
        setNewMaxInflight("");
        toast.success("Watch created.");
        await load();
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setCreating(false);
      }
    };

    const onToggle = async (watch) => {
      if (busyWatchIDs.has(watch.id)) return;
      setBusyWatchIDs((ids) => new Set(ids).add(watch.id));
      try {
        await invoke("watches.update", workspaceId, { ...watch, enabled: !watch.enabled });
        await load();
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setBusyWatchIDs((ids) => { const next = new Set(ids); next.delete(watch.id); return next; });
      }
    };

    const onDelete = async (watch) => {
      if (busyWatchIDs.has(watch.id)) return;
      setBusyWatchIDs((ids) => new Set(ids).add(watch.id));
      try {
        await invoke("watches.delete", workspaceId, { id: watch.id });
        toast.success("Watch removed.");
        await load();
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setBusyWatchIDs((ids) => { const next = new Set(ids); next.delete(watch.id); return next; });
      }
    };

    return h(
      Card,
      { id: "redmine-watchers-card" },
      h(
        CardHeader,
        null,
        h(CardTitle, null, "Issue watchers"),
        h(CardDescription, null, "Create one Kandev task per newly matching Redmine issue."),
      ),
      h(
        CardContent,
        { className: "space-y-4" },
        watches.length === 0
          ? h(Empty, { id: "redmine-watchers-empty" }, h(EmptyHeader, null, h(EmptyTitle, null, "No watches yet"), h(EmptyDescription, null, "Add one below.")))
          : h(
              Table,
              { id: "redmine-watchers-table" },
              h(
                TableHeader,
                null,
                h(TableRow, null, h(TableHead, null, "Project"), h(TableHead, null, "Max inflight"), h(TableHead, null, "Enabled"), h(TableHead, null, "")),
              ),
              h(
                TableBody,
                null,
                watches.map((watch) =>
                  h(
                    TableRow,
                    { key: watch.id },
                    h(TableCell, null, (projects.find((project) => project.id === watch.project_id) || {}).name || watch.project_id),
                    h(TableCell, null, watch.max_inflight_tasks || "unlimited"),
                    h(TableCell, null, h(Switch, { checked: watch.enabled, disabled: busyWatchIDs.has(watch.id), onCheckedChange: () => onToggle(watch) })),
                    h(TableCell, null, h(Button, { variant: "outline", size: "sm", disabled: busyWatchIDs.has(watch.id), onClick: () => onDelete(watch) }, "Delete")),
                  ),
                ),
              ),
            ),
        h(
          "div",
          { className: "grid gap-3 sm:grid-cols-2 lg:grid-cols-[1.2fr_1fr_1fr_1fr_auto] lg:items-end" },
          h(
            "div", { className: "space-y-2" },
            h(Label, { htmlFor: "redmine-new-watch-project" }, "Project"),
            h(Select, { value: newProjectId || "__select_project__", onValueChange: selectProject },
              h(SelectTrigger, { id: "redmine-new-watch-project", "data-testid": "redmine-watch-project", className: "w-full" }, h(SelectValue, { placeholder: "Select project" })),
              h(SelectContent, null, [h(SelectItem, { key: "__select_project__", value: "__select_project__" }, "Select project")].concat(projects.map((project) => h(SelectItem, { key: project.id, value: String(project.id) }, project.name)))),
            ),
          ),
          h(
            "div", { className: "space-y-2" },
            h(Label, { htmlFor: "redmine-new-watch-tracker" }, "Tracker (optional)"),
            h(Select, { value: newTrackerId || "__any_tracker__", onValueChange: (value) => setNewTrackerId(value === "__any_tracker__" ? "" : value) },
              h(SelectTrigger, { id: "redmine-new-watch-tracker", "data-testid": "redmine-watch-tracker", className: "w-full" }, h(SelectValue, null)),
              h(SelectContent, null, [h(SelectItem, { key: "__any_tracker__", value: "__any_tracker__" }, "Any tracker")].concat(trackers.map((tracker) => h(SelectItem, { key: tracker.id, value: String(tracker.id) }, tracker.name)))),
            ),
          ),
          h(
            "div", { className: "space-y-2" },
            h(Label, { htmlFor: "redmine-new-watch-status" }, "Status (optional)"),
            h(Select, { value: newStatusId || "__any_status__", onValueChange: (value) => setNewStatusId(value === "__any_status__" ? "" : value) },
              h(SelectTrigger, { id: "redmine-new-watch-status", "data-testid": "redmine-watch-status", className: "w-full" }, h(SelectValue, null)),
              h(SelectContent, null, [h(SelectItem, { key: "__any_status__", value: "__any_status__" }, "Any status")].concat(statuses.map((status) => h(SelectItem, { key: status.id, value: String(status.id) }, status.name)))),
            ),
          ),
          h(
            "div",
            { className: "space-y-2" },
            h(Label, { htmlFor: "redmine-new-watch-max" }, "Max inflight tasks"),
            h(Input, {
              id: "redmine-new-watch-max",
              "data-testid": "redmine-watch-max-inflight",
              type: "number",
              min: 0,
              placeholder: "0 = unlimited",
              value: newMaxInflight,
              onChange: (e) => setNewMaxInflight(e.target.value),
            }),
          ),
          h(Button, { id: "redmine-watchers-create", "data-testid": "redmine-watch-create", className: "w-full lg:w-auto", disabled: creating, onClick: onCreate }, creating ? "Creating…" : "Add watch"),
        ),
        newProjectId ? h(
          "div", { className: "space-y-3 rounded-md border p-3", "data-testid": "redmine-watch-filters" },
          h("div", { className: "flex items-center justify-between gap-2" }, h("div", null, h("h4", { className: "font-medium" }, "Dynamic Redmine filters"), h("p", { className: "text-muted-foreground text-sm" }, "Live options are scoped to the selected project.")), h(Button, { type: "button", variant: "outline", size: "sm", "data-testid": "redmine-watch-filters-refresh", disabled: refreshingFilters, onClick: () => { void refreshFilterOptions(); } }, refreshingFilters ? "Refreshing…" : "Refresh")),
          h("div", { className: "grid gap-3 sm:grid-cols-3" },
            h("div", { className: "space-y-2" }, h(Label, null, "Priority"), h(Select, { value: newPriorityId || "__any_priority__", onValueChange: (value) => setNewPriorityId(value === "__any_priority__" ? "" : value) }, h(SelectTrigger, { "data-testid": "redmine-watch-priority" }, h(SelectValue, null)), h(SelectContent, null, [h(SelectItem, { key: "__any_priority__", value: "__any_priority__" }, "Any priority")].concat((filterOptions.priorities || []).map((item) => h(SelectItem, { key: item.id, value: String(item.id) }, item.name)))))),
            h("div", { className: "space-y-2" }, h(Label, null, "Assigned to"), h(Select, { value: newAssigneeId || "__any_assignee__", onValueChange: (value) => setNewAssigneeId(value === "__any_assignee__" ? "" : value) }, h(SelectTrigger, { "data-testid": "redmine-watch-assignee" }, h(SelectValue, null)), h(SelectContent, null, [h(SelectItem, { key: "__any_assignee__", value: "__any_assignee__" }, "Any assignee")].concat((filterOptions.assignees || []).map((item) => h(SelectItem, { key: item.id, value: String(item.id) }, item.name)))))),
            h("div", { className: "space-y-2" }, h(Label, null, "Category"), h(Select, { value: newCategoryId || "__any_category__", onValueChange: (value) => setNewCategoryId(value === "__any_category__" ? "" : value) }, h(SelectTrigger, { "data-testid": "redmine-watch-category" }, h(SelectValue, null)), h(SelectContent, null, [h(SelectItem, { key: "__any_category__", value: "__any_category__" }, "Any category")].concat((filterOptions.categories || []).map((item) => h(SelectItem, { key: item.id, value: String(item.id) }, item.name)))))),
          ),
          h(
            "div", { className: "space-y-2" }, h(Label, null, "Add custom-field filter"),
            h(
              Select,
              { value: customFieldToAdd || "__select_custom_field__", onValueChange: (value) => { if (value !== "__select_custom_field__") setCustomFieldToAdd(value); } },
              h(SelectTrigger, { "data-testid": "redmine-watch-custom-field-add" }, h(SelectValue, { placeholder: "Select custom field" })),
              h(
                SelectContent, null,
                [h(SelectItem, { key: "__select_custom_field__", value: "__select_custom_field__" }, "Select custom field")].concat(
                  (filterOptions.custom_fields || []).filter((field) => !(String(field.id) in customFilters)).map((field) => h(SelectItem, { key: field.id, value: String(field.id) }, field.name || `Custom field #${field.id}`)),
                ),
              ),
            ),
          ),
          customFieldToAdd ? h(
            "div", { className: "flex items-end gap-2" },
            h(
              "div", { className: "flex-1 space-y-2" },
              h(Label, null, `Value for ${(filterOptions.custom_fields || []).find((field) => String(field.id) === customFieldToAdd)?.name || `custom field #${customFieldToAdd}`}`),
              h(
                Select,
                { value: customFilters[customFieldToAdd] || "__select_custom_value__", onValueChange: (value) => { if (value !== "__select_custom_value__") { setCustomFilters({ ...customFilters, [customFieldToAdd]: value }); setCustomFieldToAdd(""); } } },
                h(SelectTrigger, { "data-testid": "redmine-watch-custom-field-value" }, h(SelectValue, null)),
                h(SelectContent, null, [h(SelectItem, { key: "__select_custom_value__", value: "__select_custom_value__" }, "Select value")].concat((filterOptions.custom_field_values[customFieldToAdd] || []).map((value) => h(SelectItem, { key: value, value }, value)))),
              ),
            ),
          ) : null,
          Object.entries(customFilters).map(([id, value]) => { const field = (filterOptions.custom_fields || []).find((candidate) => String(candidate.id) === id); return h("div", { key: id, className: "flex items-center justify-between text-sm", "data-testid": `redmine-watch-custom-filter-${id}` }, h("span", null, `${field?.name || `Custom field #${id}`}: ${value}`), h(Button, { type: "button", variant: "ghost", size: "sm", onClick: () => { const next = { ...customFilters }; delete next[id]; setCustomFilters(next); } }, "Remove")); }),
        ) : null,
      ),
    );
  }

  // -- Page ----------------------------------------------------------
  return function RedmineSettingsPage({ workspaceId }) {
    const React = host.React;
    const [connection, setConnection] = React.useState(null);
    const [loading, setLoading] = React.useState(true);

    const reload = React.useCallback(async () => {
      if (!workspaceId) {
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const result = await invoke("connection.get", workspaceId, {});
        setConnection(result);
      } catch (err) {
        toast.error(errorMessage(err));
      } finally {
        setLoading(false);
      }
    }, [workspaceId]);

    React.useEffect(() => {
      reload();
    }, [reload]);

    if (!workspaceId) {
      return h(
        Empty,
        { id: "redmine-settings-no-workspace" },
        h(EmptyHeader, null, h(EmptyTitle, null, "Select a workspace"), h(EmptyDescription, null, "Redmine connections are per-workspace.")),
      );
    }
    if (loading || !connection) return h(Spinner, { id: "redmine-settings-loading" });

    const connected = connection.state === "connected" || connection.state === "degraded";

    return h(
      "div",
      { className: "space-y-4 p-4 max-w-3xl" },
      h(ConnectionSection, { workspaceId, connection, reload }),
      h(ProjectsSection, { workspaceId, connected }),
      h(FieldMappingSection, { workspaceId, connected }),
      h(SyncOptionsSection, { workspaceId, connected }),
      h(WatchersSection, { workspaceId, connected }),
    );
  };
}

// ---------------------------------------------------------------------------
// Registration. Keep only what this plugin uses.
// ---------------------------------------------------------------------------
window.registerKandevPlugin("kandev-plugin-redmine", {
  initialize(registry, host) {
    const redmineIcon = makeRedmineIcon(host);
    registry.registerTaskAction(makeLinkTaskAction(host));
    registry.registerTaskMenuAction(makeSetRedmineStatusAction(host));
    registry.registerTaskMenuAction(makeUnlinkRedmineAction(host));
    registry.registerIntegrationSettings({
      id: "redmine",
      label: "Redmine",
      description: "Link tasks to Redmine issues, sync status both ways, and watch for new issues.",
      icon: redmineIcon,
      Component: makeSettingsComponent(host),
      action: makeIntegrationEnabledAction(host),
    });
  },

  destroy() {
    // The host bulk-unregisters everything under this plugin's id; no
    // module-level state to reset (every section's state lives in React
    // component state, torn down with the component itself).
  },
});
