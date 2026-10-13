import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { AxiosError, AxiosHeaders, type InternalAxiosRequestConfig } from 'axios'
import { apiClient } from '@/api/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import AppShell from './AppShell'

vi.mock('./Header', () => ({ default: () => null }))
vi.mock('./Sidebar', () => ({ default: () => null }))

function signIn(mustChange: boolean) {
  localStorage.setItem('jarvis-admin:access_token', 'a')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem(
    'jarvis-admin:user',
    JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true, must_change_password: mustChange }),
  )
}

function renderAt(path: string) {
  render(
    <AuthProvider>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/change-password" element={<p>change password screen</p>} />
          <Route path="/login" element={<p>login screen</p>} />
          <Route element={<AppShell />}>
            <Route path="/settings" element={<p>settings page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  )
}

beforeEach(() => localStorage.clear())

describe('AppShell must-change-password gate (O4)', () => {
  it('sends a temporary-password session to the change-password screen', () => {
    signIn(true)
    renderAt('/settings')
    expect(screen.getByText('change password screen')).toBeInTheDocument()
    expect(screen.queryByText('settings page')).toBeNull()
  })

  it('lets a normal session through', () => {
    signIn(false)
    renderAt('/settings')
    expect(screen.getByText('settings page')).toBeInTheDocument()
  })

  it('sends an anonymous visitor to login', () => {
    renderAt('/settings')
    expect(screen.getByText('login screen')).toBeInTheDocument()
  })

  it('switches to the change-password screen when the server says the password must change (G8)', async () => {
    signIn(false)
    renderAt('/settings')
    expect(screen.getByText('settings page')).toBeInTheDocument()
    const adapter = apiClient.defaults.adapter
    apiClient.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
      throw new AxiosError('forbidden', 'ERR_BAD_REQUEST', config, null, {
        status: 403,
        statusText: 'Forbidden',
        headers: {},
        config: { ...config, headers: new AxiosHeaders() },
        data: { detail: 'Password change required', code: 'password_change_required' },
      })
    }
    try {
      await act(async () => {
        await apiClient.get('/api/settings').catch(() => {})
      })
    } finally {
      apiClient.defaults.adapter = adapter
    }
    expect(screen.getByText('change password screen')).toBeInTheDocument()
    expect(JSON.parse(localStorage.getItem('jarvis-admin:user') ?? '{}').must_change_password).toBe(true)
  })
})
