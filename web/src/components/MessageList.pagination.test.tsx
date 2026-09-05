import { render, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { useCallback, useState } from 'react'
import { describe, expect, it } from 'vitest'
import { MessageList } from './MessageList'
import { MessageRoleEnum, MessageStatusEnum, type Message } from '../types'

const PAGE_SIZE = 80

function record(index: number): Message {
  return {
    id: `rec-${index}`,
    role: index % 2 === 0 ? MessageRoleEnum.USER : MessageRoleEnum.ASSISTANT,
    content: `record ${index}`,
    createdAt: 1_000 + index,
    status: MessageStatusEnum.Finished,
  } as Message
}

/**
 * Owns the message list the way JobChat does: `onNeedEarlier` prepends one more
 * page and reports how many records it added.
 *
 * `newestPageSize` is deliberately larger than the render window. That is the
 * real shape of a newest page — the server extends the page start backwards
 * through an assistant/tool block so a round is never split — and it is what
 * leaves records loaded but not yet rendered above the window.
 */
function PagedTranscript({
  newestPageSize,
  totalRecords,
}: {
  newestPageSize: number
  totalRecords: number
}) {
  const [oldestLoaded, setOldestLoaded] = useState(totalRecords - newestPageSize)
  const [messages, setMessages] = useState<Message[]>(
    () => Array.from({ length: newestPageSize }, (_, i) => record(totalRecords - newestPageSize + i)),
  )

  const onNeedEarlier = useCallback(async () => {
    let added = 0
    setOldestLoaded((current) => {
      const nextOldest = Math.max(0, current - PAGE_SIZE)
      added = current - nextOldest
      if (added > 0) {
        setMessages((prev) => [
          ...Array.from({ length: added }, (_, i) => record(nextOldest + i)),
          ...prev,
        ])
      }
      return nextOldest
    })
    return added
  }, [])

  return (
    <MessageList
      messages={messages}
      isLoading={false}
      scrollContextKey="job-1"
      hasMoreEarlier={oldestLoaded > 0}
      onNeedEarlier={onNeedEarlier}
    />
  )
}

function renderedIds(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('[data-message-id]'))
    .map((node) => (node as HTMLElement).dataset.messageId ?? '')
}

describe('MessageList backwards pagination window', () => {
  it('buffers a second page after the first paint instead of waiting for the user to hit the top', async () => {
    const { container } = render(<PagedTranscript newestPageSize={PAGE_SIZE} totalRecords={400} />)

    await waitFor(() => {
      expect(renderedIds(container).length).toBeGreaterThan(PAGE_SIZE)
    })
    // Two pages loaded, and every loaded record is rendered - a record parked
    // above the window is invisible and cannot anchor the next fetch.
    const ids = renderedIds(container)
    expect(ids).toHaveLength(PAGE_SIZE * 2)
    expect(ids[0]).toBe('rec-240')
  })

  it('never leaves a prepended page parked above the render window', async () => {
    // 83 records in the newest page vs a window of 80: three records start out
    // loaded but not rendered, which is the branch that used to prepend the next
    // page WITHOUT widening the window. Everything that page carried - including
    // the top of the transcript the user was reading - silently un-rendered.
    const { container } = render(<PagedTranscript newestPageSize={83} totalRecords={163} />)

    await waitFor(() => {
      expect(renderedIds(container).length).toBeGreaterThan(83)
    })
    await waitFor(() => {
      expect(renderedIds(container)).toHaveLength(163)
    })
    expect(renderedIds(container)[0]).toBe('rec-0')
    expect(container.querySelector('[data-testid="message-history-loader"]')).toBeNull()
  })
})
