import fs from 'node:fs/promises'
import path from 'node:path'

import { expect, test } from '../fixtures/test'
import { e2eAuthHeaders, e2eFrontendURL } from '../fixtures/e2e-environment'

type AttachmentPayload = { path: string; name: string; mimeType?: string; size?: number }
type SentMessagePayload = {
  messages?: Array<{ content?: string; fileAttachments?: AttachmentPayload[] }>
}

test('uploads an ordinary file, sends its metadata, renders it, and restores it from history', async ({ page, request }) => {
  const sourceFile = path.join(test.info().outputDir, 'quartet-e2e-notes.txt')
  const sourceContents = 'Quartet ordinary attachment E2E\n'
  await fs.mkdir(path.dirname(sourceFile), { recursive: true })
  await fs.writeFile(sourceFile, sourceContents, 'utf8')

  const upload = await request.post('/api/v1/upload-file', {
    headers: e2eAuthHeaders(),
    multipart: { file: { name: path.basename(sourceFile), mimeType: 'text/plain', buffer: Buffer.from(sourceContents) } },
  })
  expect(upload.ok(), `upload failed: ${upload.status()} ${await upload.text()}`).toBeTruthy()
  const uploaded = await upload.json() as { code: number; path: string; name: string; mimeType: string; size: number }
  expect(uploaded).toMatchObject({ code: 0, name: 'quartet-e2e-notes.txt', mimeType: 'text/plain', size: Buffer.byteLength(sourceContents) })
  expect(path.isAbsolute(uploaded.path)).toBe(true)

  const download = await request.get(`/api/v1/serve-file?path=${encodeURIComponent(uploaded.path)}&name=${encodeURIComponent(uploaded.name)}`, {
    headers: e2eAuthHeaders(),
  })
  expect(download.ok(), `download failed: ${download.status()} ${await download.text()}`).toBeTruthy()
  expect(await download.text()).toBe(sourceContents)
  expect(download.headers()['content-disposition']).toContain('quartet-e2e-notes.txt')

  let sentBody: SentMessagePayload | undefined
  const jobID = 'job-attachment-e2e'
  const sessionID = 'session-attachment-e2e'
  const apiAttachment = { path: uploaded.path, name: uploaded.name, mimeType: uploaded.mimeType, size: uploaded.size }
  let historyAttachment = apiAttachment
  let historyVisible = false

  await page.route('**/api/v1/agent/list', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 0, job_enable: true, workdir: '/tmp', agent_list: [{
      agent_id: 'e2e-agent', type: 'e2e-agent', model_id: 'e2e-model', display_name: 'E2E Agent', available: true,
      models: { availableModels: [{ modelId: 'e2e-model', name: 'E2E Model' }], currentModelId: 'e2e-model' },
    }] }),
  }))
  await page.route('**/api/v1/agent/config', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: 0, models: { availableModels: [{ modelId: 'e2e-model', name: 'E2E Model' }], currentModelId: 'e2e-model' }, thoughtLevels: { availableThoughtLevels: [], currentThoughtLevelId: '' } }),
  }))
  await page.route('**/api/v1/agent/version**', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0 }),
  }))
  await page.route('**/api/v1/job/create', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ jobId: jobID, status: 'created', createdAt: Date.now() }),
  }))
  await page.route(`**/api/v1/job/${jobID}`, route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ id: jobID, title: 'Attachment E2E', status: 'completed', mode: 'interactive', workspaceId: 'ws-1', workdir: '/tmp', sessionIds: historyVisible ? [sessionID] : [], sessionCount: historyVisible ? 1 : 0, firstModelId: 'e2e-model', initialAgentId: 'e2e-agent', createdAt: Date.now(), updatedAt: Date.now(), lastEventSeq: 0 }),
  }))
  await page.route(`**/api/v1/job/${jobID}/message-queue`, route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, queue: { jobId: jobID, version: 0, paused: false, willContinue: false, items: [] } }),
  }))
  await page.route(url => url.pathname === `/api/v1/job/${jobID}/events`, route => route.fulfill({
    status: 200, contentType: 'text/event-stream', body: ': ready\n\n',
  }))
  await page.route(`**/api/v1/job/${jobID}/message`, async route => {
    sentBody = route.request().postDataJSON()
    historyVisible = true
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, status: 'started' }) })
  })
  await page.route(`**/api/v1/sessions/${sessionID}/messages`, route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ modelId: 'e2e-model', type: 'e2e-agent', workdir: '/tmp', messages: [{ id: 'attachment-message', role: 'user', content: '检查这个文件', fileAttachments: [historyAttachment], startedAt: Date.now() }] }),
  }))

  await page.addInitScript(() => localStorage.setItem('quartet-language', 'en'))
  await page.goto('/?workspaceId=ws-1')
  await expect(page.getByRole('button', { name: /upload image or file/i })).toBeVisible()

  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: /upload image or file/i }).click()
  await (await chooser).setFiles(sourceFile)
  await expect(page.getByText('quartet-e2e-notes.txt')).toBeVisible()

  await page.getByRole('textbox', { name: /ask anything/i }).fill('检查这个文件')
  await expect(page.getByTestId('home-send-button')).toBeEnabled()
  await page.getByTestId('home-send-button').click()
  await expect.poll(() => sentBody).toBeTruthy()
  expect(sentBody?.messages?.[0]).toMatchObject({
    content: '检查这个文件',
    fileAttachments: [{ name: uploaded.name, mimeType: uploaded.mimeType, size: uploaded.size }],
  })
  historyAttachment = sentBody!.messages[0].fileAttachments[0]
  expect(path.isAbsolute(historyAttachment.path)).toBe(true)
  await expect(page.getByText('quartet-e2e-notes.txt')).toBeVisible()

  await page.reload()
  await expect(page.getByText('quartet-e2e-notes.txt')).toBeVisible()
  await expect(page.getByText('检查这个文件')).toBeVisible()

  const fileLink = page.getByRole('link', { name: /quartet-e2e-notes.txt/i })
  await expect(fileLink).toHaveAttribute('download', 'quartet-e2e-notes.txt')
  const href = await fileLink.getAttribute('href')
  expect(href).toContain('/api/v1/serve-file?path=')
  expect(href).toContain('&name=quartet-e2e-notes.txt')

  const fetched = await page.evaluate(async ({ href }) => {
    const response = await fetch(href!)
    return { status: response.status, disposition: response.headers.get('content-disposition'), body: await response.text() }
  }, { href })
  expect(fetched).toEqual({ status: 200, disposition: 'attachment; filename="quartet-e2e-notes.txt"', body: sourceContents })
  expect(page.url()).toBe(`${e2eFrontendURL}/?workspaceId=ws-1&jobId=${jobID}`)
})

test('persists ordinary file metadata through the real message pipeline', async ({ request }) => {
  const headers = e2eAuthHeaders()
  let selectedAgent: { agent_id?: string; type?: string; model_id?: string; models?: { currentModelId?: string } } | undefined
  for (let attempt = 0; attempt < 20 && !selectedAgent; attempt += 1) {
    const response = await request.get('/api/v1/agent/list', { headers })
    if (response.ok()) {
      const data = await response.json()
      selectedAgent = (data.agent_list as Array<{ agent_id?: string; type?: string; model_id?: string; available?: boolean; models?: { currentModelId?: string } }> | undefined)
        ?.find(agent => agent.available && (agent.agent_id || agent.type))
    }
    if (!selectedAgent) await new Promise(resolve => setTimeout(resolve, 500))
  }
  test.skip(!selectedAgent, 'No real ACP agent is available in the isolated E2E environment')
  if (!selectedAgent) return

  const sourceContents = 'Quartet persisted attachment E2E\n'
  const upload = await request.post('/api/v1/upload-file', {
    headers,
    multipart: { file: { name: 'persisted-attachment.txt', mimeType: 'text/plain', buffer: Buffer.from(sourceContents) } },
  })
  expect(upload.ok(), `upload failed: ${upload.status()} ${await upload.text()}`).toBeTruthy()
  const uploaded = await upload.json() as { path: string; name: string; mimeType: string; size: number }
  const attachment = { path: uploaded.path, name: uploaded.name, mimeType: uploaded.mimeType, size: uploaded.size }
  const agentType = selectedAgent.agent_id || selectedAgent.type!
  const modelId = selectedAgent.models?.currentModelId || selectedAgent.model_id || ''

  const create = await request.post('/api/v1/job/create', {
    headers,
    data: { agentType, modelId, workspaceId: 'ws-1', mode: 'interactive' },
  })
  expect(create.ok(), `job create failed: ${create.status()} ${await create.text()}`).toBeTruthy()
  const jobID = (await create.json()).jobId as string
  const messageID = `attachment-${Date.now()}`
  const send = await request.post(`/api/v1/job/${jobID}/message`, {
    headers,
    data: {
      clientMessageId: messageID,
      messages: [{ id: messageID, type: 'text', role: 'user', timestamp: Date.now(), content: '请确认收到附件。', fileAttachments: [attachment] }],
      agentType,
      modelId,
    },
  })
  expect(send.ok(), `message send failed: ${send.status()} ${await send.text()}`).toBeTruthy()

  let persisted: { content?: string; fileAttachments?: Array<Record<string, unknown>> } | undefined
  await expect.poll(async () => {
    const detailResponse = await request.get(`/api/v1/job/${jobID}`, { headers })
    if (!detailResponse.ok()) return false
    const detail = await detailResponse.json() as { sessionIds?: string[] }
    const sessionID = detail.sessionIds?.at(-1)
    if (!sessionID) return false
    const historyResponse = await request.get(`/api/v1/sessions/${sessionID}/messages`, { headers })
    if (!historyResponse.ok()) return false
    const history = await historyResponse.json() as { messages?: Array<{ id?: string; content?: string; fileAttachments?: Array<Record<string, unknown>> }> }
    persisted = history.messages?.find(message => message.id === messageID)
    return Boolean(persisted)
  }, { timeout: 30_000 }).toBe(true)

  expect(persisted).toMatchObject({ content: '请确认收到附件。', fileAttachments: [attachment] })
})
