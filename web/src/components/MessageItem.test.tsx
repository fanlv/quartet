import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { MessageItem } from './MessageItem'
import { MessageRoleEnum, MessageStatusEnum, type SystemMessage } from '../types'

const JOB_ID = 'job-1'

function wsListMessage(): SystemMessage {
  return {
    id: 'sys-1',
    role: MessageRoleEnum.SYSTEM,
    content: '可用工作空间：\n1. ws-alpha\n*2. ws-beta',
    status: MessageStatusEnum.Finished,
    createdAt: Date.now(),
    commandSource: '/ws',
  } as SystemMessage
}

function stubCommandFetch(handler: (url: string) => Response) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.includes(`/job/${JOB_ID}/message`)) return handler(url)
    throw new Error(`Unexpected fetch in test: ${url}`)
  }))
}

describe('MessageItem system command bubble', () => {
  it('clicking a /ws list row applies the inline command event: action dispatch + text toast', async () => {
    const actionEvents: Array<Record<string, unknown>> = []
    window.addEventListener('quartet:command-action', ((e: CustomEvent) => {
      actionEvents.push(e.detail as Record<string, unknown>)
    }) as EventListener)

    stubCommandFetch(() => new Response(JSON.stringify({
      code: 0,
      status: 'command_dispatched',
      event: {
        type: 'command_system_message',
        command: '/workspace',
        text: '已切换到工作空间: ws-alpha',
        present: 'toast',
        action: { type: 'switch_workspace', workspaceId: 'ws-alpha' },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))

    render(<MessageItem message={wsListMessage()} jobId={JOB_ID} />)
    fireEvent.click(screen.getByTitle('运行 /ws 1'))

    // The inline event's action must reach App.tsx's listener — a terminal
    // job's SSE is torn down by design, so the transient broadcast alone
    // would land on no reader and the click would look dead.
    await waitFor(() => expect(actionEvents).toHaveLength(1))
    expect(actionEvents[0]).toMatchObject({ type: 'switch_workspace', workspaceId: 'ws-alpha' })
    await waitFor(() => expect(document.querySelector('.copy-toast')?.textContent).toContain('已切换到工作空间'))
  })

  it('surfaces the server error when the command POST fails', async () => {
    stubCommandFetch(() => new Response(JSON.stringify({ error: 'job is deleted' }), {
      status: 409,
      headers: { 'Content-Type': 'application/json' },
    }))

    render(<MessageItem message={wsListMessage()} jobId={JOB_ID} />)
    fireEvent.click(screen.getByTitle('运行 /ws 1'))

    await waitFor(() => expect(document.querySelector('.copy-toast')?.textContent).toContain('job is deleted'))
  })
})
