import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { GeneralSettings } from './GeneralSettings'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

function mockFetchByRoute(routes: Record<string, Response | (() => Response | Promise<Response>)>) {
  vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input)
    const path = url.startsWith('http') ? new URL(url).pathname : url.split('?')[0]
    const route = routes[path]
    if (!route) throw new Error(`Unexpected fetch in GeneralSettings test: ${url}`)
    return typeof route === 'function' ? route() : route
  })
}

describe('GeneralSettings i18n behavior', () => {
  it('updates visible settings copy after switching language', async () => {
    const user = userEvent.setup()
    mockFetchByRoute({
      '/api/v1/config/settings/get': jsonResponse({
        code: 0,
        settings: {
          username: 'User',
          avatar_url: '',
        },
      }),
      '/api/v1/agent/list': jsonResponse({ agent_list: [] }),
      '/api/v1/agent/catalog': jsonResponse({ code: 0, agents: [] }),
      '/api/v1/config/settings/title-generation-agent': jsonResponse({ code: 0, config: null }),
      '/api/v1/config/settings/group-reply-agent': jsonResponse({ code: 0, config: null }),
      '/api/v1/config/settings/im-session-agent': jsonResponse({ code: 0, config: null }),
      '/api/v1/config/settings/language': jsonResponse({ code: 0 }),
    })

    render(<GeneralSettings />)

    expect(await screen.findByText('User Settings')).toBeTruthy()
    expect(screen.getByText('Select the interface language. It takes effect immediately and is stored in the instance settings, shared by every domain and device')).toBeTruthy()

    await user.selectOptions(screen.getByRole('combobox'), 'zh')

    expect(await screen.findByText('用户配置')).toBeTruthy()
    expect(screen.getByText('选择界面显示语言，立即生效并保存到实例配置，各域名与设备共用同一选择')).toBeTruthy()
    expect(screen.queryByText('User Settings')).toBeNull()
    await waitFor(() => expect(document.documentElement.lang).toBe('zh'))
    expect(localStorage.getItem('quartet-language')).toBe('zh')
  })
})
