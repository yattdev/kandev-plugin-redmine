import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./ui/e2e",
  testMatch: /packaged-plugin\.spec\.ts/,
  timeout: 90_000,
  globalSetup: "./ui/e2e/require-packaged-host.mjs",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  use: {
    baseURL: process.env.KANDEV_PLUGIN_E2E_URL,
    trace: "on-first-retry",
    viewport: { width: 1440, height: 1000 },
  },
});
