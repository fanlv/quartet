import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatInput } from './ChatInput'

const PLACEHOLDER = 'Type a message'

function renderChatInput(overrides: Partial<Parameters<typeof ChatInput>[0]> = {}) {
  const props: Parameters<typeof ChatInput>[0] = {
    onSend: vi.fn(),
    isLoading: false,
    placeholder: PLACEHOLDER,
    ...overrides,
  }
  render(<ChatInput {...props} />)
  return {
    textarea: screen.getByPlaceholderText(PLACEHOLDER) as HTMLTextAreaElement,
    onSend: props.onSend as ReturnType<typeof vi.fn>,
  }
}

function writeHistory(scope: string, contents: string[]) {
  localStorage.setItem(
    `quartet:sent_history:${scope}`,
    JSON.stringify({
      v: 1,
      items: contents.map((content, index) => ({
        id: `history-${index}`,
        ts: Date.now() - index,
        content,
      })),
    }),
  )
}

describe('ChatInput keyboard behavior', () => {
  beforeEach(() => {
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      callback(0)
      return 0
    })
  })

  it('sends trimmed text with Enter and clears the textarea', async () => {
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, '  hello world  ')
    await user.keyboard('{Enter}')

    expect(onSend).toHaveBeenCalledTimes(1)
    expect(onSend).toHaveBeenCalledWith('hello world', undefined)
    expect(textarea).toHaveValue('')
  })

  it('inserts a newline with Ctrl/Cmd+Enter instead of sending', async () => {
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, 'hello')
    textarea.setSelectionRange(5, 5)
    fireEvent.keyDown(textarea, { key: 'Enter', ctrlKey: true })

    await waitFor(() => expect(textarea).toHaveValue('hello\n'))
    expect(onSend).not.toHaveBeenCalled()

    await user.type(textarea, 'world')
    textarea.setSelectionRange(11, 11)
    fireEvent.keyDown(textarea, { key: 'Enter', metaKey: true })

    await waitFor(() => expect(textarea).toHaveValue('hello\nworld\n'))
    expect(onSend).not.toHaveBeenCalled()
  })

  it('keeps Shift+Enter as textarea newline behavior', async () => {
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, 'hello')
    await user.keyboard('{Shift>}{Enter}{/Shift}')
    await user.type(textarea, 'world')

    expect(textarea).toHaveValue('hello\nworld')
    expect(onSend).not.toHaveBeenCalled()
  })

  it('ignores Enter while IME composition is active', async () => {
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, '拼')
    fireEvent.keyDown(textarea, { key: 'Enter', keyCode: 229 })

    expect(onSend).not.toHaveBeenCalled()
    expect(textarea).toHaveValue('拼')
  })

  it('navigates slash-command completion and completes the active command', async () => {
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, '/w')
    expect(screen.getByText('/workspace')).toBeTruthy()
    expect(screen.getByText('/ws')).toBeTruthy()

    await user.keyboard('{ArrowDown}')
    await user.keyboard('{Enter}')

    expect(textarea).toHaveValue('/ws ')
    expect(screen.queryByText('/workspace')).toBeNull()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('recalls sent-message history with ArrowUp and restores with ArrowDown', async () => {
    writeHistory('chat-input-history', ['newest message', 'older message'])
    const { textarea, onSend } = renderChatInput({ localHistoryKey: 'chat-input-history' })

    textarea.focus()
    textarea.setSelectionRange(0, 0)
    fireEvent.keyDown(textarea, { key: 'ArrowUp' })
    await waitFor(() => expect(textarea).toHaveValue('newest message'))

    fireEvent.keyDown(textarea, { key: 'ArrowUp' })
    await waitFor(() => expect(textarea).toHaveValue('older message'))

    fireEvent.keyDown(textarea, { key: 'ArrowDown' })
    await waitFor(() => expect(textarea).toHaveValue('newest message'))

    fireEvent.keyDown(textarea, { key: 'ArrowDown' })
    await waitFor(() => expect(textarea).toHaveValue(''))
    expect(onSend).not.toHaveBeenCalled()
  })
})
