import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { AuthProvider } from '@/auth/AuthContext'
import { refreshAccessToken } from '@/api/client'
import AccountPage from './AccountPage'

const auth = vi.hoisted(() => ({
  updateMe: vi.fn(),
  listHouseholds: vi.fn(),
  refresh: vi.fn(),
  switchHousehold: vi.fn(),
}))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), ...auth }))

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  auth.listHouseholds.mockResolvedValue([])
  localStorage.setItem('jarvis-admin:access_token', jwt('h1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', username: 'emm', is_superuser: false }))
})

describe('AccountPage', () => {
  it('shows the name and saves a display name through PATCH /auth/me', async () => {
    auth.updateMe.mockResolvedValue({ id: 7, email: 'm@example.com', username: 'emm', display_name: 'Emma', is_superuser: false })
    render(
      <AuthProvider>
        <AccountPage />
      </AuthProvider>,
    )
    expect(screen.getByTestId('account-name')).toHaveTextContent('emm')
    fireEvent.change(screen.getByLabelText(/Display name/), { target: { value: '  Emma ' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    })
    expect(auth.updateMe).toHaveBeenCalledWith('Emma')
    expect(screen.getByTestId('account-name')).toHaveTextContent('Emma')
    expect(JSON.parse(localStorage.getItem('jarvis-admin:user') ?? '{}').display_name).toBe('Emma')
  })
})

describe('token refresh keeps the picked household', () => {
  it('re-scopes a refreshed token to the household the user switched to', async () => {
    localStorage.setItem('jarvis-admin:household', 'h2')
    auth.refresh.mockResolvedValue({ access_token: jwt('h1'), refresh_token: 'r2' })
    auth.switchHousehold.mockResolvedValue({ access_token: jwt('h2'), household_id: 'h2' })
    render(
      <AuthProvider>
        <p>x</p>
      </AuthProvider>,
    )
    let tok: string | null = null
    await act(async () => {
      tok = await refreshAccessToken()
    })
    expect(auth.switchHousehold).toHaveBeenCalledWith('h2', jwt('h1'))
    expect(tok).toBe(jwt('h2'))
    expect(localStorage.getItem('jarvis-admin:access_token')).toBe(jwt('h2'))
  })

  it('drops a household the user no longer belongs to', async () => {
    localStorage.setItem('jarvis-admin:household', 'gone')
    auth.refresh.mockResolvedValue({ access_token: jwt('h1'), refresh_token: 'r2' })
    auth.switchHousehold.mockRejectedValue(new Error('403'))
    render(
      <AuthProvider>
        <p>x</p>
      </AuthProvider>,
    )
    let tok: string | null = null
    await act(async () => {
      tok = await refreshAccessToken()
    })
    expect(tok).toBe(jwt('h1'))
    expect(localStorage.getItem('jarvis-admin:household')).toBeNull()
  })
})
