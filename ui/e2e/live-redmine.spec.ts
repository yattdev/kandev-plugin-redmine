import path from "node:path";
import { expect, test } from "@playwright/test";
import {
  invokeAction,
  mutationHeaders,
  pluginID,
  requiredEnvironment,
  responseJSON,
  workspaceID,
  type JsonRecord,
} from "./helpers";

const screenshotDir = path.resolve("docs/screenshots");

test("configures the per-workspace Redmine connection and captures validation UI", async ({
  page,
  request,
}) => {
  const workspaceId = await workspaceID(request);
  const baseURL = requiredEnvironment("KANDEV_REDMINE_E2E_BASE_URL");
  const apiKey = requiredEnvironment("KANDEV_REDMINE_E2E_API_KEY");
  const rotatedAPIKey = requiredEnvironment("KANDEV_REDMINE_E2E_ROTATED_API_KEY");

  const connected = await invokeAction(request, workspaceId, "connection.save", {
    base_url: baseURL,
    api_key: apiKey,
  });
  expect(connected).toMatchObject({ state: "connected", base_url: baseURL });
  expect(JSON.stringify(connected)).not.toContain(apiKey);
  const rotated = await invokeAction(request, workspaceId, "connection.save", {
    base_url: baseURL,
    api_key: rotatedAPIKey,
  });
  expect(rotated).toMatchObject({ state: "connected", base_url: baseURL });
  expect(JSON.stringify(rotated)).not.toContain(rotatedAPIKey);

  const projects = await invokeAction(request, workspaceId, "projects.list");
  const visibleProjects = Array.isArray(projects.projects) ? (projects.projects as JsonRecord[]) : [];
  expect(visibleProjects.length).toBeGreaterThan(0);
  const projectID = Number(visibleProjects[0].id);
  await expect(
    invokeAction(request, workspaceId, "projects.save", { project_ids: [projectID] }),
  ).resolves.toEqual({ saved: true });

  const mapping = await invokeAction(request, workspaceId, "fieldmapping.get");
  expect(Array.isArray(mapping.live_statuses)).toBeTruthy();
  expect(Array.isArray(mapping.live_trackers)).toBeTruthy();
  expect(Array.isArray(mapping.live_priorities)).toBeTruthy();
  expect(Array.isArray(mapping.custom_fields)).toBeTruthy();
  expect((mapping.custom_fields as JsonRecord[]).some((field) => String(field.name).length > 0)).toBeTruthy();

  const workflows = await responseJSON(
    await request.get(`/api/v1/workspaces/${workspaceId}/workflows`),
  );
  const workflow = (workflows.workflows as JsonRecord[])[0];
  const workflowID = String(workflow.id);
  const workflowSteps = await responseJSON(
    await request.get(`/api/v1/workflows/${workflowID}/workflow/steps`),
  );
  const steps = workflowSteps.steps as JsonRecord[];
  expect(steps.length).toBeGreaterThan(1);
  const firstStep = steps[0];
  const secondStep = steps[1];
  const workflowStepID = String(firstStep.id);
  const secondWorkflowStepID = String(secondStep.id);
  const liveStatuses = mapping.live_statuses as JsonRecord[];
  const liveTrackers = mapping.live_trackers as JsonRecord[];
  const livePriorities = mapping.live_priorities as JsonRecord[];
  expect(liveStatuses.length).toBeGreaterThan(1);
  const statusID = Number(liveStatuses[0].id);
  const closedStatus = liveStatuses.find((status) => status.is_closed === true);
  expect(closedStatus, "the disposable Redmine instance must expose a closed status").toBeTruthy();
  const secondStatusID = Number(closedStatus?.id);
  const trackerID = Number(liveTrackers[0].id);
  const priorityID = Number(livePriorities[0].id);

  await expect(
    invokeAction(request, workspaceId, "fieldmapping.save", {
      workflow_id: workflowID,
      statuses: liveStatuses.map((status, index) => ({
        redmine_status_id: Number(status.id),
        workflow_step_id:
          index === 0
            ? workflowStepID
            : Number(status.id) === secondStatusID
              ? secondWorkflowStepID
              : "",
      })),
      trackers: liveTrackers.map((tracker, index) => ({
        redmine_tracker_id: Number(tracker.id),
        task_label: index === 0 ? "redmine-live" : "",
      })),
      priorities: livePriorities.map((priority, index) => ({
        redmine_priority_id: Number(priority.id),
        task_priority: index === 0 ? "high" : "",
      })),
    }),
  ).resolves.toEqual({ saved: true });

  const uploaded = await invokeAction(request, workspaceId, "issues.upload", {
    filename: "redmine-live-validation.txt",
    content_type: "text/plain",
    content_base64: Buffer.from("Redmine plugin live validation").toString("base64"),
  });
  expect(typeof uploaded.token).toBe("string");
  expect(uploaded).toMatchObject({
    filename: "redmine-live-validation.txt",
    content_type: "text/plain",
  });
  const issueSubject = `Kandev Redmine live validation ${Date.now()}`;
  const createdIssue = await invokeAction(request, workspaceId, "issues.create", {
    project_id: projectID,
    tracker_id: trackerID,
    status_id: statusID,
    priority_id: priorityID,
    subject: issueSubject,
    description: "Created by the plugin-owned packaged acceptance suite.",
    uploads: [uploaded],
  });
  const issueID = Number(createdIssue.id);
  expect(issueID).toBeGreaterThan(0);
  const redmineIssueResponse = async () =>
    responseJSON(
      await request.get(`${baseURL}/issues/${issueID}.json?include=attachments`, {
        headers: { "X-Redmine-API-Key": rotatedAPIKey },
      }),
    );
  const issueWithAttachment = (await redmineIssueResponse()).issue as JsonRecord;
  const attachments = issueWithAttachment.attachments as JsonRecord[];
  expect(attachments.some((attachment) => attachment.filename === uploaded.filename)).toBeTruthy();
  await expect(
    invokeAction(request, workspaceId, "issues.update", {
      issue_id: issueID,
      subject: `${issueSubject} updated`,
    }),
  ).resolves.toEqual({ updated: true });

  const headers = await mutationHeaders(request);
  const task = await responseJSON(
    await request.post("/api/v1/tasks", {
      headers,
      data: {
        workspace_id: workspaceId,
        workflow_id: workflowID,
        workflow_step_id: workflowStepID,
        title: "Redmine live linked task",
        description: "Plugin-owned E2E task",
      },
    }),
  );
  const taskID = String(task.id);
  await expect(
    invokeAction(request, workspaceId, "link.set", { reference: `#${issueID}` }, taskID),
  ).resolves.toMatchObject({ linked: true, issue_id: issueID });
  await expect(invokeAction(request, workspaceId, "link.get", {}, taskID)).resolves.toMatchObject({
    linked: true,
    issue_id: issueID,
  });

  await expect(
    invokeAction(request, workspaceId, "syncoptions.save", {
      auto_status_writeback: false,
      sync_title_description: true,
    }),
  ).resolves.toEqual({ saved: true });
  const inboundTitle = `Inbound Redmine update ${Date.now()}`;
  await expect(
    invokeAction(request, workspaceId, "issues.update", {
      issue_id: issueID,
      status_id: secondStatusID,
      subject: inboundTitle,
    }),
  ).resolves.toEqual({ updated: true });
  await expect
    .poll(async () => {
      const updatedTask = await responseJSON(await request.get(`/api/v1/tasks/${taskID}`));
      return { title: updatedTask.title, workflowStepID: updatedTask.workflow_step_id };
    })
    .toEqual({ title: inboundTitle, workflowStepID: secondWorkflowStepID });

  // Disable/re-enable preserves connection, mapping, link, and cursor. A
  // second inbound update proves the restarted poller resumes persisted state.
  expect((await request.post(`/api/plugins/${pluginID}/disable`)).ok()).toBeTruthy();
  expect((await request.post(`/api/plugins/${pluginID}/enable`)).ok()).toBeTruthy();
  const resumedTitle = `Resumed Redmine update ${Date.now()}`;
  await expect(
    invokeAction(request, workspaceId, "issues.update", {
      issue_id: issueID,
      subject: resumedTitle,
    }),
  ).resolves.toEqual({ updated: true });
  await expect
    .poll(async () => {
      const updatedTask = await responseJSON(await request.get(`/api/v1/tasks/${taskID}`));
      return updatedTask.title;
    })
    .toBe(resumedTitle);

  await expect(
    invokeAction(request, workspaceId, "syncoptions.save", {
      auto_status_writeback: true,
      sync_title_description: true,
    }),
  ).resolves.toEqual({ saved: true });
  const moved = await request.post(`/api/v1/tasks/${taskID}/move`, {
    headers,
    data: { workflow_id: workflowID, workflow_step_id: workflowStepID, position: 0 },
  });
  expect(moved.ok(), await moved.text()).toBeTruthy();
  await expect
    .poll(async () => {
      const redmineIssue = (await redmineIssueResponse()).issue as JsonRecord;
      return Number((redmineIssue.status as JsonRecord).id);
    })
    .toBe(statusID);
  // Give the inbound overlap poll another turn; echo suppression must leave
  // the task in the step that originated the Redmine write-back.
  const echoCheckStartedAt = Date.now();
  await expect
    .poll(async () => {
      if (Date.now() - echoCheckStartedAt < 3_500) return "waiting-for-overlap-poll";
      const updatedTask = await responseJSON(await request.get(`/api/v1/tasks/${taskID}`));
      return updatedTask.workflow_step_id;
    }, { timeout: 10_000 })
    .toBe(workflowStepID);

  await expect(
    invokeAction(request, workspaceId, "link.set_status", { status_id: secondStatusID }, taskID),
  ).resolves.toEqual({ pushed: true });
  await expect(invokeAction(request, workspaceId, "link.unset", {}, taskID)).resolves.toEqual({
    unlinked: true,
  });

  // A second Kandev workspace must not inherit the first one's endpoint or
  // credential. This exercises the host-verified workspace action scope and
  // the plugin's separator-safe workspace secret key composition.
  const isolatedName = `Redmine E2E isolation ${Date.now()}`;
  const isolated = await responseJSON(
    await request.post("/api/v1/workspaces", { headers, data: { name: isolatedName } }),
  );
  const isolatedID = String(isolated.id);
  try {
    await expect(invokeAction(request, isolatedID, "connection.get")).resolves.toEqual({
      state: "disconnected",
    });
  } finally {
    const deleted = await request.delete(`/api/v1/workspaces/${isolatedID}`, {
      headers,
      data: { confirm_name: isolatedName },
    });
    expect(deleted.ok(), await deleted.text()).toBeTruthy();
  }

  await page.goto(`/settings/workspaces/${workspaceId}/integrations/redmine`);
  await expect(page.locator("#redmine-connection-state")).toHaveText("connected");
  await expect(page.getByTestId("redmine-api-key-input")).toHaveValue("");
  await expect(page.getByTestId("redmine-projects-save")).toBeVisible();
  await page.screenshot({ path: path.join(screenshotDir, "redmine-connected-settings.png"), fullPage: true });

  await expect(page.locator("#redmine-projects-card")).toBeVisible();
  await page.locator("#redmine-projects-card").screenshot({
    path: path.join(screenshotDir, "redmine-project-selection.png"),
  });
  await expect(page.locator("#redmine-fieldmapping-card")).toBeVisible();
  await page.locator("#redmine-fieldmapping-card").screenshot({
    path: path.join(screenshotDir, "redmine-field-mapping.png"),
  });
  await expect(page.locator("#redmine-watchers-card")).toBeVisible();
  await page.locator("#redmine-watchers-card").screenshot({
    path: path.join(screenshotDir, "redmine-issue-watchers.png"),
  });

  // A failed/interrupted local run can leave a watch in plugin state. Remove
  // those definitions through the public action before measuring the new
  // watch so reruns remain deterministic without touching host storage.
  const existingWatches = await invokeAction(request, workspaceId, "watches.list");
  for (const existing of (existingWatches.watches as JsonRecord[] | undefined) ?? []) {
    await invokeAction(request, workspaceId, "watches.delete", { id: String(existing.id) });
  }

  const beforeWatch = await responseJSON(
    await request.get(`/api/v1/workspaces/${workspaceId}/tasks`),
  );
  const beforeTaskIDs = new Set(
    (beforeWatch.tasks as JsonRecord[]).map((candidate) => String(candidate.id)),
  );
  const createdWatch = await invokeAction(request, workspaceId, "watches.create", {
    project_id: projectID,
    tracker_id: trackerID,
    status_id: null,
    max_inflight_tasks: 1,
    enabled: true,
  });
  const watchID = String(createdWatch.id);
  const polledWatch = await invokeAction(request, workspaceId, "watches.poll");
  expect(polledWatch).toEqual({ polled: true });
  let watcherTaskIDs: string[] = [];
  await expect
    .poll(
      async () => {
        const listed = await responseJSON(
          await request.get(`/api/v1/workspaces/${workspaceId}/tasks`),
        );
        watcherTaskIDs = (listed.tasks as JsonRecord[])
          .map((candidate) => String(candidate.id))
          .filter((id) => !beforeTaskIDs.has(id));
        return watcherTaskIDs.length;
      },
      { timeout: 20_000, intervals: [1_000, 2_000] },
    )
    .toBe(1);
  expect(await invokeAction(request, workspaceId, "watches.poll")).toEqual({ polled: true });
  const afterDuplicatePoll = await responseJSON(
    await request.get(`/api/v1/workspaces/${workspaceId}/tasks`),
  );
  const afterDuplicateIDs = (afterDuplicatePoll.tasks as JsonRecord[])
    .map((candidate) => String(candidate.id))
    .filter((id) => !beforeTaskIDs.has(id));
  expect(afterDuplicateIDs).toEqual(watcherTaskIDs);
  await expect(
    invokeAction(request, workspaceId, "watches.delete", { id: watchID }),
  ).resolves.toEqual({ deleted: true });
  await expect
    .poll(async () => {
      const listed = await responseJSON(
        await request.get(`/api/v1/workspaces/${workspaceId}/tasks`),
      );
      const current = new Set((listed.tasks as JsonRecord[]).map((candidate) => String(candidate.id)));
      return watcherTaskIDs.filter((id) => current.has(id)).length;
    })
    .toBe(0);
});
