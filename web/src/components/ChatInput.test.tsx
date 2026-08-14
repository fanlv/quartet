import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatInput } from './ChatInput'
import { __resetSkillsCacheForTest } from '../utils/skills'

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
    expect(textarea.selectionStart).toBe('/ws '.length)
    expect(screen.queryByText('/workspace')).toBeNull()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('lists skills in the slash floater, inserts the picked skill, and highlights it', async () => {
    __resetSkillsCacheForTest()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/skills/list')) {
        return {
          json: async () => ({
            code: 0,
            skills: [{ name: 'pptx', path: '/root/.agents/skills/pptx', scope: 'global', agents: [] }],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    await user.type(textarea, '/p')
    // Skill row shows basic info (name + path) once the list arrives.
    expect(await screen.findByText('/pptx')).toBeTruthy()
    expect(screen.getByText('/root/.agents/skills/pptx')).toBeTruthy()

    await user.keyboard('{Enter}')

    expect(textarea).toHaveValue('/pptx ')
    expect(textarea.selectionStart).toBe('/pptx '.length)
    expect(screen.queryByText('/root/.agents/skills/pptx')).toBeNull()
    expect(onSend).not.toHaveBeenCalled()
    // Selected skill renders as a chip in the highlight backdrop.
    const chip = document.querySelector('.chat-skill-chip')
    expect(chip).toBeTruthy()
    expect(chip?.textContent).toBe('/pptx')
  })

  it('surfaces the upload error when sending is blocked by a failed image upload', async () => {
    URL.createObjectURL = vi.fn(() => 'blob:mock-preview')
    URL.revokeObjectURL = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/api/v1/upload-file')) {
        return { json: async () => ({ code: 1, msg: 'disk full' }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))
    const user = userEvent.setup()
    const { textarea, onSend } = renderChatInput()

    // Attach an image; the upload fails and the preview chip shows the error badge.
    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(fileInput, {
      target: { files: [new File(['x'], 'pic.png', { type: 'image/png' })] },
    })
    expect(await screen.findByTitle('disk full')).toBeTruthy()

    // The send button stays enabled (only `uploading` disables it), so the user clicks it…
    await user.type(textarea, 'hello')
    const sendButton = screen.getByTestId('chat-send-button')
    expect(sendButton).toBeEnabled()
    await user.click(sendButton)

    // …the message must not go out with a broken attachment, and the user must be
    // told why. Before the fix both assertions failed: the click was a silent no-op.
    expect(onSend).not.toHaveBeenCalled()
    await waitFor(() => {
      expect(document.querySelector('.copy-toast')?.textContent).toContain('disk full')
    })
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
