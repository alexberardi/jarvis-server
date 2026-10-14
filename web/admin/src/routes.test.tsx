import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders, type InternalAxiosRequestConfig } from 'axios'
import { AuthProvider } from '@/auth/AuthContext'
import { ThemeProvider } from '@/theme/ThemeProvider'
import AppShell from '@/components/layout/AppShell'
import { apiClient } from '@/api/client'
import { shellRoutes } from '@/routes'

const auth = vi.hoisted(() => ({
  listHouseholds: vi.fn(),
  switchHousehold: vi.fn(),
  getSetupState: vi.fn(),
  logout: vi.fn(),
}))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), ...auth }))
vi.mock('@/hooks/useSystem', () => ({ useSystemInfo: () => ({ data: undefined }) }))
vi.mock('@/hooks/useInbox', () => ({ useUnreadCount: () => ({ data: 3 }) }))
// The admin pages themselves are covered elsewhere; here only where they mount matters.
vi.mock('@/pages/DashboardPage', () => ({ default: () => <p>dashboard page</p> }))
vi.mock('@/pages/SettingsPage', () => ({ default: () => <p>settings page</p> }))
vi.mock('@/pages/TraceDetailPage', () => ({ default: () => <p>trace detail page</p> }))
vi.mock('@/pages/ConnectionsPage', () => ({ default: () => <p>connections page</p> }))
vi.mock('@/pages/ChatPage', () => ({ default: () => <h1>Chat</h1> }))
vi.mock('@/pages/UsersPage', () => ({ default: () => <p>users page</p> }))

/** A fake JWT whose payload carries the household claim, as auth mints them. */
function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '1', household_id: household })}.sig`
}

function signIn(user: { id?: number; is_superuser: boolean; must_change_password?: boolean; display_name?: string }, hh = 'h1') {
  localStorage.setItem('jarvis-admin:access_token', jwt(hh))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', username: 'emm', ...user }))
}

function Where() {
  const loc = useLocation()
  return <output data-testid="where">{loc.pathname + loc.search}</output>
}

function renderAt(path: string) {
  localStorage.setItem('jarvis-admin-theme', 'light')
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <MemoryRouter initialEntries={[path]}>
            <Where />
            <Routes>
              <Route path="/login" element={<p>login screen</p>} />
              <Route path="/change-password" element={<p>change password screen</p>} />
              <Route element={<AppShell />}>{shellRoutes()}</Route>
            </Routes>
          </MemoryRouter>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>,
  )
}

const nav = () => screen.getByRole('complementary', { name: 'Main navigation' })
const where = () => screen.getByTestId('where').textContent

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  vi.clearAllMocks()
  auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'member' }])
  auth.getSetupState.mockResolvedValue({ superuser: true, setup_completed: true, live_ready: true })
  auth.logout.mockResolvedValue(undefined)
})

describe('member shell (webapp 01 P2)', () => {
  it('shows a member Chat and no Admin group', async () => {
    signIn({ is_superuser: false })
    renderAt('/chat')
    expect(within(nav()).getByRole('link', { name: 'Chat' })).toBeInTheDocument()
    expect(within(nav()).getByRole('link', { name: 'Add-ons' })).toHaveAttribute('href', '/addons')
    expect(within(nav()).getByRole('link', { name: /Inbox/ })).toHaveAttribute('href', '/inbox')
    expect(within(nav()).getByLabelText('3 unread')).toBeInTheDocument()
    expect(within(nav()).queryByText('Admin')).toBeNull()
    expect(within(nav()).queryByRole('link', { name: 'Dashboard' })).toBeNull()
    expect(within(nav()).queryByRole('link', { name: 'Settings' })).toBeNull()
    await act(async () => {})
  })

  it('shows a superuser the Admin group under /admin', async () => {
    signIn({ is_superuser: true })
    renderAt('/chat')
    expect(within(nav()).getByText('Admin')).toBeInTheDocument()
    expect(within(nav()).getByRole('link', { name: 'Dashboard' })).toHaveAttribute('href', '/admin/dashboard')
    expect(within(nav()).getByRole('link', { name: 'Updates' })).toHaveAttribute('href', '/admin/update')
    await act(async () => {})
  })

  it('gives a member following an admin link the notice, not the page', async () => {
    signIn({ is_superuser: false })
    renderAt('/admin/settings')
    expect(screen.getByText("This page is for the install's administrator")).toBeInTheDocument()
    expect(screen.queryByText('settings page')).toBeNull()
    await act(async () => {})
  })

  it('gives a member the notice through an old admin path too', async () => {
    signIn({ is_superuser: false })
    renderAt('/users')
    expect(await screen.findByText("This page is for the install's administrator")).toBeInTheDocument()
    expect(where()).toBe('/admin/users')
    expect(screen.queryByText('users page')).toBeNull()
  })

  it.each([
    ['/dashboard', '/admin/dashboard', 'dashboard page'],
    ['/settings', '/admin/settings', 'settings page'],
    ['/traces/abc?tab=raw', '/admin/traces/abc?tab=raw', 'trace detail page'],
    ['/services', '/admin/connections', 'connections page'],
    ['/admin', '/admin/dashboard', 'dashboard page'],
  ])('redirects %s to %s for a superuser', async (from, to, page) => {
    signIn({ is_superuser: true })
    renderAt(from)
    expect(await screen.findByText(page)).toBeInTheDocument()
    expect(where()).toBe(to)
  })

  it('sends a member home to Chat', async () => {
    signIn({ is_superuser: false })
    renderAt('/')
    expect(await screen.findByRole('heading', { name: 'Chat' })).toBeInTheDocument()
    expect(where()).toBe('/chat')
  })

  it('sends a superuser home to the dashboard while setup is unfinished', async () => {
    auth.getSetupState.mockResolvedValue({ superuser: true, setup_completed: false, live_ready: true })
    signIn({ is_superuser: true })
    renderAt('/')
    expect(await screen.findByText('dashboard page')).toBeInTheDocument()
  })

  it('sends a superuser home to Chat once everything is ready', async () => {
    signIn({ is_superuser: true })
    renderAt('/')
    expect(await screen.findByRole('heading', { name: 'Chat' })).toBeInTheDocument()
  })

  it('puts a member with a temporary password on the change screen before anything else (G8)', async () => {
    signIn({ is_superuser: false, must_change_password: true })
    renderAt('/chat')
    expect(screen.getByText('change password screen')).toBeInTheDocument()
    // No household call while every route but the change answers 403.
    await act(async () => {})
    expect(auth.listHouseholds).not.toHaveBeenCalled()
  })

  it('moves to the change screen when the server answers password_change_required', async () => {
    signIn({ is_superuser: false })
    renderAt('/chat')
    await act(async () => {})
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
        await apiClient.get('/api/cc/api/v0/admin/nodes').catch(() => {})
      })
    } finally {
      apiClient.defaults.adapter = adapter
    }
    expect(screen.getByText('change password screen')).toBeInTheDocument()
  })

  it('shows the display name in the header', async () => {
    signIn({ is_superuser: false, display_name: 'Emma' })
    renderAt('/chat')
    expect(screen.getAllByText('Emma').length).toBeGreaterThan(0)
    await act(async () => {})
  })
})

describe('household switcher (§4.4)', () => {
  it('is hidden with one household', async () => {
    signIn({ is_superuser: false })
    renderAt('/chat')
    await act(async () => {})
    expect(auth.listHouseholds).toHaveBeenCalled()
    expect(screen.queryByRole('combobox', { name: 'Household' })).toBeNull()
  })

  it('appears with two and switches the session', async () => {
    auth.listHouseholds.mockResolvedValue([
      { id: 'h1', name: 'Home', role: 'member' },
      { id: 'h2', name: 'Cabin', role: 'admin' },
    ])
    auth.switchHousehold.mockResolvedValue({ access_token: jwt('h2'), household_id: 'h2' })
    signIn({ is_superuser: false })
    renderAt('/chat')
    // One in the nav drawer (phones) and one in the header (sm and up).
    const [inDrawer, select] = await screen.findAllByRole('combobox', { name: 'Household' })
    expect(within(nav()).getByRole('combobox', { name: 'Household' })).toBe(inDrawer)
    expect(select).toHaveValue('h1')
    await act(async () => {
      fireEvent.change(select, { target: { value: 'h2' } })
    })
    expect(auth.switchHousehold).toHaveBeenCalledWith('h2')
    expect(select).toHaveValue('h2')
    expect(inDrawer).toHaveValue('h2')
    expect(localStorage.getItem('jarvis-admin:access_token')).toBe(jwt('h2'))
    expect(localStorage.getItem('jarvis-admin:household')).toBe('h2')
  })
})

describe('logout (G13)', () => {
  it('clears the chat state and the remembered choices', async () => {
    signIn({ is_superuser: false })
    sessionStorage.setItem('jarvis-admin:chat:7', '{"messages":[]}')
    sessionStorage.setItem('unrelated', 'kept')
    localStorage.setItem('jarvis-admin:household', 'h1')
    localStorage.setItem('jarvis-admin:u:chat-node:h1', 'node-1')
    renderAt('/chat')
    await act(async () => {})
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Log out' }))
    })
    expect(screen.getByText('login screen')).toBeInTheDocument()
    expect(sessionStorage.getItem('jarvis-admin:chat:7')).toBeNull()
    expect(sessionStorage.getItem('unrelated')).toBe('kept')
    expect(localStorage.getItem('jarvis-admin:household')).toBeNull()
    expect(localStorage.getItem('jarvis-admin:u:chat-node:h1')).toBeNull()
    expect(localStorage.getItem('jarvis-admin-theme')).toBe('light')
  })
})
