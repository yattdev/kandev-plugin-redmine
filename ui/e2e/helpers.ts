import { expect, type APIRequestContext, type APIResponse } from "@playwright/test";

export const pluginID = "kandev-plugin-redmine";

export type JsonRecord = Record<string, unknown>;

export async function responseJSON(response: APIResponse): Promise<JsonRecord> {
  const text = await response.text();
  expect(response.ok(), text).toBeTruthy();
  return text ? (JSON.parse(text) as JsonRecord) : {};
}

export async function invokeAction(
  request: APIRequestContext,
  workspaceId: string,
  key: string,
  body: JsonRecord = {},
  taskId?: string,
): Promise<JsonRecord> {
  return responseJSON(
    await request.post(`/api/plugins/${pluginID}/actions/${key}`, {
      data: { workspaceId, ...(taskId ? { taskId } : {}), body },
    }),
  );
}

export async function workspaceID(request: APIRequestContext): Promise<string> {
  const configured = process.env.KANDEV_REDMINE_E2E_WORKSPACE_ID?.trim();
  const payload = await responseJSON(await request.get("/api/v1/workspaces"));
  const workspaces = Array.isArray(payload.workspaces) ? payload.workspaces : [];
  const ids = workspaces.flatMap((workspace) => {
    if (!workspace || typeof workspace !== "object") return [];
    const id = (workspace as JsonRecord).id;
    return typeof id === "string" ? [id] : [];
  });
  if (configured && ids.includes(configured)) return configured;
  if (configured) throw new Error(`Configured workspace ${configured} is not present on the host`);
  if (ids.length !== 1) {
    throw new Error("Set KANDEV_REDMINE_E2E_WORKSPACE_ID unless the disposable host has exactly one workspace");
  }
  return ids[0];
}

export async function mutationHeaders(request: APIRequestContext): Promise<Record<string, string>> {
  const response = await request.get("/api/v1/app-state?path=%2Fsettings%2Fagents");
  const payload = await responseJSON(response);
  const token = payload.interimSettingsInterlockToken;
  if (typeof token !== "string" || !token) throw new Error("Kandev interlock token is missing");
  return { "X-Kandev-Interim-Settings-Interlock": token };
}

export function requiredEnvironment(name: string): string {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
