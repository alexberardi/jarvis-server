import { afterEach, describe, expect, it, vi } from 'vitest'
import type { InternalAxiosRequestConfig } from 'axios'
import { apiClient } from '@/api/client'
import { setup } from '@/api/auth'
import { captureSetupToken, clearSetupToken, getSetupToken, SETUP_TOKEN_HEADER } from './setupToken'

function fakeLocation(pathname: string, hash: string, search = ''): Location {
  return { pathname, hash, search } as Location
}

function fakeHistory() {
  return { state: { k: 1 }, replaceState: vi.fn() } as unknown as History & { replaceState: ReturnType<typeof vi.fn> }
}

afterEach(() => {
  clearSetupToken()
  sessionStorage.clear()
})

describe('captureSetupToken', () => {
  it('reads #token= on /setup and strips it from the address bar', () => {
    const hist = fakeHistory()
    const tok = captureSetupToken(fakeLocation('/setup', '#token=abc_DEF-123'), hist)
    expect(tok).toBe('abc_DEF-123')
    expect(getSetupToken()).toBe('abc_DEF-123')
    expect(hist.replaceState).toHaveBeenCalledWith({ k: 1 }, '', '/setup')
  })

  it('keeps other fragment params and the query', () => {
    const hist = fakeHistory()
    captureSetupToken(fakeLocation('/setup/', '#token=t1&x=2', '?a=1'), hist)
    expect(hist.replaceState).toHaveBeenCalledWith({ k: 1 }, '', '/setup/?a=1#x=2')
  })

  it('ignores the fragment on any other path', () => {
    const hist = fakeHistory()
    expect(captureSetupToken(fakeLocation('/login', '#token=nope'), hist)).toBeNull()
    expect(getSetupToken()).toBeNull()
    expect(hist.replaceState).not.toHaveBeenCalled()
  })

  it('ignores /setup without a token', () => {
    const hist = fakeHistory()
    expect(captureSetupToken(fakeLocation('/setup', ''), hist)).toBeNull()
    expect(hist.replaceState).not.toHaveBeenCalled()
  })

  it('survives a reload of the tab through sessionStorage', () => {
    captureSetupToken(fakeLocation('/setup', '#token=persist'), fakeHistory())
    expect(sessionStorage.getItem('jarvis-admin:setup-token')).toBe('persist')
  })
})

describe('api setup', () => {
  it('sends the token as X-Jarvis-Setup-Token on POST /api/auth/setup', async () => {
    let seen: InternalAxiosRequestConfig | undefined
    const original = apiClient.defaults.adapter
    apiClient.defaults.adapter = async (config) => {
      seen = config
      return {
        data: { access_token: 'a', refresh_token: 'r', token_type: 'bearer', user: { id: 1, email: 'e', is_superuser: true } },
        status: 201,
        statusText: 'Created',
        headers: {},
        config,
      }
    }
    try {
      await setup('admin@example.com', 'password1', 'Admin', 'tok-123')
    } finally {
      apiClient.defaults.adapter = original
    }
    expect(seen?.url).toBe('/api/auth/setup')
    expect(seen?.method).toBe('post')
    expect(seen?.headers.get(SETUP_TOKEN_HEADER)).toBe('tok-123')
    expect(JSON.parse(seen?.data as string)).toEqual({ email: 'admin@example.com', password: 'password1', username: 'Admin' })
  })
})
