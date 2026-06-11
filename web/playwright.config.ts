import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  fullyParallel: false,
  use: {
    baseURL: "http://127.0.0.1:5175",
    trace: "on-first-retry",
  },
  webServer: {
    command: "corepack yarn dev --host 127.0.0.1 --port 5175 --strictPort",
    url: "http://127.0.0.1:5175/extension-ui-harness.html",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
