import type { Page } from '../fixtures/test'
import { expect, test } from '../fixtures/test'
import { e2eAuthToken } from '../fixtures/e2e-environment'

// E2E coverage for the settings page's eino tab: the tab proxies
// /api/v1/config/eino/* to `eino-cli models|systemprompt` subcommands exec'd
// by the backend, with state living in the run's isolated Memory config store.
//
// The e2e environment only puts eino-cli on the backend's PATH (and seeds one
// model into an isolated LOCAL_MEMORY) when QUARTET_E2E_MODEL_API_KEY is
// supplied — see e2e-environment.ts. So this spec runs in two modes:
//
//   - eino-cli unavailable (default run): the tab must surface the backend's
//     exec error in full with a retry affordance, never a blank pane.
//   - eino-cli available (credentials run): the seeded model lists, the
//     catalog round-trips through the UI (add + delete), the system prompt
//     saves and persists, and the seeded API key is never rendered
//     (keys stay masked inside the eino-cli process).
//
// Availability is probed once via the tab's own API rather than env flags, so
// the spec self-selects the right assertions. The CRUD test additionally
// requires the seeded catalog (detected via the E2E display name) because its
// assertions start from exactly one known model.

let einoAvailable = false
let einoSeeded = false

test.beforeAll(async ({ request }) => {
  const res = await request.get('/api/v1/config/eino/model/list', {
    headers: { 'X-AGENT-AUTH': e2eAuthToken },
  })
  const body = (await res.json().catch(() => ({}))) as { code?: number; models?: { display_name?: string }[] }
  einoAvailable = res.ok() && body.code === 0
  einoSeeded = einoAvailable && Array.isArray(body.models) && body.models.some((m) => m.display_name === 'Quartet E2E Model')
})

async function openEinoTab(page: Page) {
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto('/')
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)

  await page.getByTestId('settings-open-button').click()
  await expect(page.getByTestId('settings-modal')).toBeVisible()
  await page.getByTestId('settings-tab').filter({ hasText: 'Eino' }).click()
  await expect(page.getByTestId('settings-content')).toHaveAttribute('data-active-tab', 'eino')
}

test('eino tab surfaces the backend error in full when eino-cli is unavailable', async ({ page }) => {
  test.skip(einoAvailable, 'eino-cli is on PATH in this run; error path covered implicitly')

  await openEinoTab(page)

  await expect(page.getByTestId('eino-model-error')).toBeVisible()
  // The full exec error (eino-cli lookup failure) must reach the user,
  // not a swallowed generic message — per the project's error-display rule.
  await expect(page.getByTestId('eino-model-error')).toContainText(/eino-cli/)
  await expect(page.getByTestId('eino-model-error').getByRole('button')).toBeVisible()
})

test('eino tab round-trips the model catalog and system prompt', async ({ page }) => {
  test.skip(!einoAvailable, 'eino-cli is not on PATH in this run (no QUARTET_E2E_MODEL_API_KEY)')
  test.skip(einoAvailable && !einoSeeded, 'the isolated E2E model catalog was not seeded')

  await openEinoTab(page)

  // Seeded catalog (one model written into LOCAL_MEMORY by globalSetup) lists.
  const cards = page.getByTestId('eino-model-card')
  await expect(cards).toHaveCount(1)
  await expect(cards.first()).toContainText('Quartet E2E Model')

  // The seeded API key must never be rendered — keys stay masked inside the
  // eino-cli process and only the masked form crosses the wire.
  const seededKey = process.env.QUARTET_E2E_MODEL_API_KEY || ''
  if (seededKey) {
    await expect(page.getByTestId('settings-modal')).not.toContainText(seededKey)
  }

  // Add a model through the form (catalog write only — no provider call).
  await page.getByTestId('eino-add-model-toggle').click()
  await page.getByTestId('eino-form-display-name').fill('E2E Added Model')
  await page.getByTestId('eino-form-model').fill('e2e-added-model')
  await page.getByTestId('eino-form-api-key').fill('e2e-dummy-key')
  await page.getByTestId('eino-form-submit').click()
  await expect(cards).toHaveCount(2)
  await expect(cards.filter({ hasText: 'E2E Added Model' })).toHaveCount(1)

  // Delete it again (confirm dialog accepted) so the catalog is left seeded-only.
  page.once('dialog', (dialog) => void dialog.accept())
  await cards.filter({ hasText: 'E2E Added Model' }).getByTestId('eino-model-delete').click()
  await expect(cards).toHaveCount(1)

  // System prompt: save a marker, verify it survives a full page reload,
  // then restore the original prompt so later specs see the seeded default.
  const promptInput = page.getByTestId('eino-system-prompt-input')
  const originalPrompt = await promptInput.inputValue()
  await promptInput.fill('E2E marker system prompt')
  await page.getByTestId('eino-system-prompt-save').click()
  await expect(page.getByTestId('eino-prompt-message')).toBeVisible()

  await openEinoTab(page)
  await expect(page.getByTestId('eino-system-prompt-input')).toHaveValue('E2E marker system prompt')

  await page.getByTestId('eino-system-prompt-input').fill(originalPrompt)
  await page.getByTestId('eino-system-prompt-save').click()
  await expect(page.getByTestId('eino-prompt-message')).toBeVisible()
})
