// The admin SPA's E2E suite. Run it through scripts/e2e web, which brings up the sandboxes,
// exports their URLs (sandbox.env) and E2E_OUT, and turns results.json into the run's report
// (docs/dev/e2e.md).
import { defineConfig, devices } from '@playwright/test'
import path from 'node:path'
import { outDir } from './helpers/env'

export default defineConfig({
  testDir: './tests',
  // One sandbox, one scripted model (one active script) and one auth rate limit for the
  // whole run: tests run one at a time. Each makes its own throwaway users and items, so the
  // order does not matter.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  // Room for one wait-out of the auth rate limit (helpers/login.ts, helpers/api.ts).
  timeout: 120_000,
  expect: { timeout: 10_000 },
  forbidOnly: true,
  outputDir: path.join(outDir(), 'artifacts'),
  globalSetup: './global-setup.ts',
  reporter: [['list'], ['./reporter.ts']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.SBX_ADMIN,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    viewport: { width: 1440, height: 900 },
  },
})
