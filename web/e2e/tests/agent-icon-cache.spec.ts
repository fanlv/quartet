import { expect, test } from '../fixtures/test'
import { e2eAuthToken } from '../fixtures/e2e-environment'

// Verifies that all agent icon rendering paths display correctly after the
// icon proxy cache feature. Icons served from /api/v1/icon?url=... must render
// as <img> elements (not raw text), and emoji icons must still render as text.

async function openAppWithAuth(page: import('@playwright/test').Page) {
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto('/')
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: /ask anything/i })).toBeVisible()
}

test.describe('agent icon rendering', () => {
  test('agent selector shows icons as images, not raw text', async ({ page }) => {
    await openAppWithAuth(page)

    // Open the agent dropdown
    await page.locator('.chat-model-selector .model-tag').first().click()
    await expect(page.locator('.model-dropdown')).toBeVisible()

    // Every dropdown item with an icon should render either an <img> or an emoji <span>
    const items = page.locator('.model-dropdown-item')
    const count = await items.count()
    expect(count).toBeGreaterThan(0)

    for (let i = 0; i < count; i++) {
      const item = items.nth(i)
      const img = item.locator('img.model-dropdown-icon')
      const emoji = item.locator('span.model-dropdown-emoji')
      const placeholder = item.locator('div.model-dropdown-icon-placeholder')

      // Each item must have exactly one of: img, emoji span, or placeholder
      const imgCount = await img.count()
      const emojiCount = await emoji.count()
      const placeholderCount = await placeholder.count()
      expect(imgCount + emojiCount + placeholderCount,
        `item ${i} must have an icon (img, emoji, or placeholder)`).toBeGreaterThanOrEqual(1)

      // If it has an img, verify it loaded successfully (not broken)
      if (imgCount > 0) {
        const src = await img.getAttribute('src')
        expect(src).toBeTruthy()
        // The src should be either a proxy URL or a direct URL
        expect(
          src!.startsWith('/api/v1/icon') || src!.startsWith('http'),
          `img src should be a proxy or http URL, got: ${src}`
        ).toBeTruthy()

        // Wait for the image to load (proxy may need time for the first fetch)
        await expect.poll(async () => {
          return await img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)
        }, { timeout: 10_000, message: `icon image for item ${i} should load (src=${src})` }).toBeTruthy()
      }

      // If it has an emoji span, verify it's not a URL being rendered as text
      if (emojiCount > 0) {
        const text = await emoji.textContent()
        expect(text).toBeTruthy()
        expect(
          !text!.startsWith('http') && !text!.startsWith('/api/'),
          `emoji span should not contain a URL, got: ${text}`
        ).toBeTruthy()
      }
    }
  })

  test('selected agent tag shows icon correctly', async ({ page }) => {
    await openAppWithAuth(page)

    // The model-tag (selected agent display) should render icon properly
    const tag = page.locator('.chat-model-selector .model-tag').first()
    await expect(tag).toBeVisible()

    const tagImg = tag.locator('img.model-tag-icon')
    const tagEmoji = tag.locator('span.model-tag-emoji')

    const hasImg = await tagImg.count() > 0
    const hasEmoji = await tagEmoji.count() > 0

    if (hasImg) {
      const src = await tagImg.getAttribute('src')
      expect(src).toBeTruthy()
      expect(
        src!.startsWith('/api/v1/icon') || src!.startsWith('http'),
        `tag img src should be a proxy or http URL, got: ${src}`
      ).toBeTruthy()

      await expect.poll(async () => {
        return await tagImg.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)
      }, { timeout: 10_000, message: `selected agent tag icon should load (src=${src})` }).toBeTruthy()
    }

    if (hasEmoji) {
      const text = await tagEmoji.textContent()
      expect(
        !text!.startsWith('http') && !text!.startsWith('/api/'),
        `tag emoji should not be a URL: ${text}`
      ).toBeTruthy()
    }
  })

  test('icon proxy endpoint returns image content-type', async ({ request }) => {
    // Get the agent list to find a real icon URL
    const listRes = await request.get('/api/v1/agent/list', {
      headers: { 'X-AGENT-AUTH': e2eAuthToken },
    })
    expect(listRes.ok()).toBeTruthy()
    const data = await listRes.json()
    const agents = (data.agent_list || []) as Array<{ icon_url: string; display_name: string }>

    // Find an agent with a proxy icon URL
    const proxyAgent = agents.find((a) => a.icon_url?.startsWith('/api/v1/icon'))
    if (!proxyAgent) {
      test.skip(true, 'No agents with proxy icon URLs found')
      return
    }

    // Hit the proxy endpoint
    const iconRes = await request.get(proxyAgent.icon_url, {
      headers: { 'X-AGENT-AUTH': e2eAuthToken },
    })

    // Should return successfully with an image content type
    expect(iconRes.ok(), `icon proxy returned ${iconRes.status()} for ${proxyAgent.display_name}`).toBeTruthy()
    const contentType = iconRes.headers()['content-type'] || ''
    expect(
      contentType.startsWith('image/'),
      `icon proxy should return image content-type, got: ${contentType}`
    ).toBeTruthy()

    // Should have cache headers
    const cacheControl = iconRes.headers()['cache-control'] || ''
    expect(cacheControl).toContain('max-age=')
  })
})
