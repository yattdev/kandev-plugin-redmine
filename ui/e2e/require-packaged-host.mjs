import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const pluginRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

export default async function requirePackagedHost() {
  const baseURL = process.env.KANDEV_PLUGIN_E2E_URL?.replace(/\/$/, "");
  if (!baseURL) {
    throw new Error(
      "KANDEV_PLUGIN_E2E_URL is required and must name a disposable compatible Kandev host.",
    );
  }

  const health = await fetch(`${baseURL}/health`, { signal: AbortSignal.timeout(10_000) });
  if (!health.ok) throw new Error(`Disposable Kandev host is unhealthy (${health.status})`);

  await mkdir(path.join(pluginRoot, "docs/screenshots"), { recursive: true });

  const manifest = await readFile(path.join(pluginRoot, "manifest.yaml"), "utf8");
  const id = manifest.match(/^id: "([^"]+)"$/m)?.[1];
  const version = manifest.match(/^version: "([^"]+)"$/m)?.[1];
  if (!id || !version) throw new Error("manifest.yaml must declare id and version");

  const packagePath = process.env.KANDEV_PLUGIN_E2E_PACKAGE ?? path.join(pluginRoot, `${id}-${version}.tar.gz`);
  const archive = await readFile(packagePath).catch((error) => {
    throw new Error(`Run make package-host first; cannot read ${packagePath}: ${String(error)}`);
  });

  // The runner's contract requires a disposable host. Remove an earlier
  // candidate installation so repeated local contract/live runs test the
  // package supplied for this invocation, never stale extracted bits.
  await fetch(`${baseURL}/api/plugins/${id}`, {
    method: "DELETE",
    signal: AbortSignal.timeout(30_000),
  }).catch(() => undefined);
  const form = new FormData();
  form.set("package", new Blob([archive], { type: "application/gzip" }), path.basename(packagePath));
  const response = await fetch(`${baseURL}/api/plugins/install`, {
    method: "POST",
    body: form,
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) {
    throw new Error(`Could not install ${path.basename(packagePath)} (${response.status}): ${await response.text()}`);
  }
  const installed = await response.json();
  if (installed?.plugin?.id !== id || installed?.plugin?.status !== "active") {
    throw new Error(`Packaged plugin did not become active: ${JSON.stringify(installed)}`);
  }
}
