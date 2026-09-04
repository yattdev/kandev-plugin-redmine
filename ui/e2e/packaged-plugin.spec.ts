import { expect, test } from "@playwright/test";
import { invokeAction, pluginID, workspaceID } from "./helpers";

test("installs the package and renders safe unconfigured host UI", async ({ page, request }) => {
  const workspaceId = await workspaceID(request);

  await expect(await invokeAction(request, workspaceId, "connection.get")).toEqual({ state: "disconnected" });
  await expect(await invokeAction(request, workspaceId, "watches.list")).toEqual({ watches: [] });

  await page.goto(`/settings/workspaces/${workspaceId}/integrations/redmine`);
  await expect(page.getByTestId("redmine-base-url-input")).toBeVisible();
  await expect(page.getByTestId("redmine-api-key-input")).toBeVisible();
  await expect(page.getByTestId("redmine-connection-save")).toBeDisabled();
  await expect(page.locator("#redmine-connection-state")).toHaveText("disconnected");

  await page.goto("/settings/plugins");
  const row = page.getByTestId(`plugin-row-${pluginID}`);
  await expect(row).toBeVisible();
  await expect(row.getByText("Active", { exact: true })).toBeVisible();
});
