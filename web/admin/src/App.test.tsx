import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider, useAuth } from '@/auth/AuthContext'
import { AppRoutes } from '@/App'

const auth = vi.hoisted(() => ({
  getSetupState: vi.fn(),
  setup: vi.fn(),
  logout: vi.fn(),
  listHouseholds: vi.fn(),
}))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), ...auth }))
vi.mock('@/pages/LoginPage', () => ({ default: () => <p>login screen</p> }))
// The wizard's Account step signs the new superuser in; the rest of the wizard is not needed here.
vi.mock('@/pages/SetupWizard', () => ({
  default: function Wizard() {
    const { setup, state } = useAuth()
    if (state.isAuthenticated) return <Navigate to="/chat" replace />
    return (
      <button type="button" onClick={() => void setup('owner@example.com', 'Owner-Pass-123')}>
        create superuser
      </button>
    )
  },
}))
vi.mock('@/components/layout/AppShell', () => ({
  default: function Shell() {
    const { state, logout } = useAuth()
    if (!state.isAuthenticated) return <Navigate to="/login" replace />
    return (
      <button type="button" onClick={logout}>
        log out
      </button>
    )
  },
}))

function Where() {
  const loc = useLocation()
  return <output data-testid="where">{loc.pathname}</output>
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  vi.clearAllMocks()
  auth.getSetupState.mockResolvedValue({ needs_superuser: true })
  auth.setup.mockResolvedValue({ access_token: 'a.e30.s', refresh_token: 'r', token_type: 'bearer', user: { id: 1, email: 'owner@example.com', is_superuser: true } })
  auth.logout.mockResolvedValue(undefined)
  auth.listHouseholds.mockResolvedValue([])
})

describe('boot gate (S1)', () => {
  it('sends a fresh install to setup, and a sign-out after setup to the login page', async () => {
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AuthProvider>
          <MemoryRouter initialEntries={['/login']}>
            <Where />
            <AppRoutes />
          </MemoryRouter>
        </AuthProvider>
      </QueryClientProvider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'create superuser' }))
    expect(await screen.findByRole('button', { name: 'log out' })).toBeInTheDocument()
    expect(screen.getByTestId('where')).toHaveTextContent('/chat')

    // The install has its superuser now: signing out leads to the login page, not back
    // into the wizard (which bounced between /setup and /login and showed a blank page).
    fireEvent.click(screen.getByRole('button', { name: 'log out' }))
    expect(await screen.findByText('login screen')).toBeInTheDocument()
    expect(screen.getByTestId('where')).toHaveTextContent('/login')
  })
})
