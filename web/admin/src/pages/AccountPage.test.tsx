import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
const household = vi.hoisted(() => ({ getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn() }))
vi.mock('@/api/household', async (orig) => ({ ...(await orig<typeof import('@/api/household')>()), ...household }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <AccountPage />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  auth.listHouseholds.mockResolvedValue([])
  household.getHouseholdSettings.mockResolvedValue({})
  localStorage.setItem('jarvis-admin:access_token', jwt('h1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', username: 'emm', is_superuser: false }))
})

describe('AccountPage', () => {
  it('shows the name and saves a display name through PATCH /auth/me', async () => {
    auth.updateMe.mockResolvedValue({ id: 7, email: 'm@example.com', username: 'emm', display_name: 'Emma', is_superuser: false })
    renderPage()
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

describe('AccountPage household settings', () => {
  it('lets a household admin turn watches and errand autonomy on', async () => {
    auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'admin' }])
    household.getHouseholdSettings.mockResolvedValue({ 'watches.enabled': false, 'errands.autonomous_enabled': false })
    household.putHouseholdSetting.mockResolvedValue(undefined)
    renderPage()
    const watches = await screen.findByRole('switch', { name: 'Watches' })
    const errands = screen.getByRole('switch', { name: 'Errand autonomy' })
    await waitFor(() => expect(watches).toBeEnabled())
    expect(watches).not.toBeChecked()
    expect(errands).not.toBeChecked()
    expect(screen.getAllByText(/Off by default/)).toHaveLength(2)
    expect(screen.queryByText(/Only a household admin can change/)).toBeNull()

    await act(async () => {
      fireEvent.click(watches)
    })
    expect(household.putHouseholdSetting).toHaveBeenCalledWith('h1', 'watches.enabled', true)
    await waitFor(() => expect(watches).toBeChecked())
    await act(async () => {
      fireEvent.click(errands)
    })
    expect(household.putHouseholdSetting).toHaveBeenCalledWith('h1', 'errands.autonomous_enabled', true)
    await waitFor(() => expect(errands).toBeChecked())
  })

  it('shows a member the values but not the switches to change them', async () => {
    auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'member' }])
    household.getHouseholdSettings.mockResolvedValue({ 'watches.enabled': true, 'errands.autonomous_enabled': false })
    renderPage()
    const watches = await screen.findByRole('switch', { name: 'Watches' })
    await waitFor(() => expect(watches).toBeChecked())
    expect(watches).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Errand autonomy' })).toBeDisabled()
    expect(screen.getByText('Only a household admin can change these.')).toBeVisible()
    fireEvent.click(watches)
    expect(household.putHouseholdSetting).not.toHaveBeenCalled()
  })

  it('leaves the switch as it was when the write fails', async () => {
    auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'owner' }])
    household.getHouseholdSettings.mockResolvedValue({ 'watches.enabled': false, 'errands.autonomous_enabled': false })
    household.putHouseholdSetting.mockRejectedValue(new Error('nope'))
    renderPage()
    const watches = await screen.findByRole('switch', { name: 'Watches' })
    await waitFor(() => expect(watches).toBeEnabled())
    await act(async () => {
      fireEvent.click(watches)
    })
    expect(household.putHouseholdSetting).toHaveBeenCalled()
    expect(watches).not.toBeChecked()
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
