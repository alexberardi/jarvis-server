import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import { ThemeProvider } from '@/theme/ThemeProvider'
import AppShell from './AppShell'

// The footer's system info needs the API; the drawer doesn't.
vi.mock('@/hooks/useSystem', () => ({ useSystemInfo: () => ({ data: undefined }) }))
vi.mock('@/hooks/useInbox', () => ({ useUnreadCount: () => ({ data: 0 }) }))

function renderShell() {
  localStorage.setItem('jarvis-admin-theme', 'light')
  localStorage.setItem('jarvis-admin:access_token', 'a')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem(
    'jarvis-admin:user',
    JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true, must_change_password: false }),
  )
  render(
    <ThemeProvider>
      <AuthProvider>
        <MemoryRouter initialEntries={['/admin/dashboard']}>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/admin/dashboard" element={<p>dashboard page</p>} />
              <Route path="/admin/logs" element={<p>logs page</p>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </ThemeProvider>,
  )
}

const menu = () => screen.getByRole('button', { name: 'Open navigation' })
const drawer = () => screen.getByRole('complementary', { name: 'Main navigation' })

beforeEach(() => localStorage.clear())

describe('navigation drawer (below md)', () => {
  it('starts closed and opens from the header menu button', () => {
    renderShell()
    expect(menu()).toHaveAttribute('aria-expanded', 'false')
    expect(menu()).toHaveAttribute('aria-controls', drawer().id)
    expect(drawer()).toHaveAttribute('data-open', 'false')
    expect(screen.queryByTestId('sidebar-backdrop')).toBeNull()

    fireEvent.click(menu())
    expect(menu()).toHaveAttribute('aria-expanded', 'true')
    expect(drawer()).toHaveAttribute('data-open', 'true')
    expect(screen.getByTestId('sidebar-backdrop')).toBeInTheDocument()
  })

  it('closes with its close button, the backdrop and Escape', () => {
    renderShell()
    fireEvent.click(menu())
    fireEvent.click(screen.getByRole('button', { name: 'Close navigation' }))
    expect(drawer()).toHaveAttribute('data-open', 'false')

    fireEvent.click(menu())
    fireEvent.click(screen.getByTestId('sidebar-backdrop'))
    expect(drawer()).toHaveAttribute('data-open', 'false')

    fireEvent.click(menu())
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(drawer()).toHaveAttribute('data-open', 'false')
  })

  it('closes when a destination is picked, and navigates there', () => {
    renderShell()
    fireEvent.click(menu())
    fireEvent.click(screen.getByRole('link', { name: 'Logs' }))
    expect(screen.getByText('logs page')).toBeInTheDocument()
    expect(drawer()).toHaveAttribute('data-open', 'false')
    expect(menu()).toHaveAttribute('aria-expanded', 'false')
  })

  it('keeps the sidebar a static column from md up', () => {
    renderShell()
    // jsdom has no layout; the contract is the responsive classes.
    expect(drawer().className).toMatch(/\bmd:static\b/)
    expect(drawer().className).toMatch(/\bmd:translate-x-0\b/)
    expect(menu().className).toMatch(/\bmd:hidden\b/)
  })
})
