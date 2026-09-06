import { defineConfig, devices } from '@playwright/test'

const frontendPort = Number(process.env.VITE_E2E_PORT || 5174)
const workers = Number(process.env.QUARTET_E2E_WORKERS || 4)

if (!Number.isInteger(workers) || workers < 1) {
  throw new Error(`Invalid QUARTET_E2E_WORKERS: ${process.env.QUARTET_E2E_WORKERS}`)
}

export default defineConfig({
  testDir: './tests',
  globalSetup: './fixtures/e2e-environment.ts',
  timeout: 30_000,
  expect: {
    timeout: 5_000,
  },
  fullyParallel: true,
  workers,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'test-results/html' }]],
  outputDir: 'test-results/artifacts',
  use: {
    baseURL: `http://127.0.0.1:${frontendPort}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: process.env.QUARTET_E2E_VIDEO === '1' ? 'retain-on-failure' : 'off',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
