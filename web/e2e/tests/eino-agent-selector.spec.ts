import { expect, test } from '../fixtures/test'

// E2E coverage for the refactor's core selector regression (spec user story
// 1): eino-cli must appear as ONE ordinary agent entry in the agent selector
// — exactly like other ACP agents — never one row per configured model.
//
// Requires the credentials run (QUARTET_E2E_MODEL_API_KEY): only then does
// globalSetup put eino-cli on the backend's PATH and seed its isolated
// catalog. Default runs self-skip, mirroring eino-settings.spec.ts.
//
// Story 2 (in-entry model dropdown switch) is not asserted here: the e2e seed
// installs a single model and the dropdown only renders with >1 candidates —
// that check belongs to the credentials acceptance run (issue 32).

let einoSeeded = false

test.beforeAll(async ({ request }) => {
  const res = await request.get('/api/v1/config/eino/model/list', {
  })
  const body = (await res.json().catch(() => ({}))) as { code?: number; models?: { display_name?: string }[] }
  einoSeeded = res.ok() && body.code === 0 && Array.isArray(body.models) && body.models.some((m) => m.display_name === 'Quartet E2E Model')
})

test('agent selector shows eino-cli as a single entry', async ({ page }) => {
  test.skip(!einoSeeded, 'eino-cli is not seeded in this run (no QUARTET_E2E_MODEL_API_KEY)')

  await page.addInitScript(() => {
    localStorage.setItem('quartet-language', 'en')
  })
  await page.goto('/')
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)

  // Open the agent dropdown on the home page composer and count eino entries.
  // Pre-refactor each configured eino model inflated its own row; now exactly
  // one "Eino" entry (the probe display name) may exist.
  await page.locator('.chat-model-selector .model-tag').first().click()

  const einoEntries = page.locator('.model-dropdown-item').filter({ hasText: 'Eino' })
  await expect(einoEntries).toHaveCount(1)
})
