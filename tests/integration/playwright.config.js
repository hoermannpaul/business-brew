const { defineConfig } = require("@playwright/test");

module.exports = defineConfig({
  testDir: ".",
  testMatch: "landing.spec.js",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  outputDir: "test-results",
  reporter: [["line"], ["junit", { outputFile: "test-results/results.xml" }]],
  use: {
    baseURL: "http://127.0.0.1:8080",
    browserName: "chromium",
    viewport: { width: 1440, height: 1000 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure"
  },
  webServer: {
    command: "node static-server.js",
    url: "http://127.0.0.1:4173/business-brew/",
    timeout: 10_000,
    reuseExistingServer: false
  }
});
