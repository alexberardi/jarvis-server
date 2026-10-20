import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import LoginPage from './LoginPage'

vi.mock('@/api/auth', async (orig) => ({
  ...(await orig<typeof import('@/api/auth')>()),
  getSetupStatus: vi.fn().mockResolvedValue({ needs_setup: false }),
  login: vi.fn().mockRejectedValue(new Error('Invalid email or password')),
}))

function renderLogin() {
  render(
    <AuthProvider>
      <MemoryRouter initialEntries={['/login']}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
        </Routes>
      </MemoryRouter>
    </AuthProvider>,
  )
}

beforeEach(() => localStorage.clear())

describe('LoginPage', () => {
  // A10 rehearsal: signing in landed on Settings (the old admin's home), not the Dashboard.
  // It goes through /setup, which resumes an unfinished wizard (A10 F9) or forwards to the
  // dashboard (SetupWizard.test.tsx covers both).
  it('sends a signed-in operator on through /setup', async () => {
    localStorage.setItem('jarvis-admin:access_token', 'a')
    localStorage.setItem('jarvis-admin:refresh_token', 'r')
    localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true }))
    render(
      <AuthProvider>
        <MemoryRouter initialEntries={['/login']}>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/setup" element={<p>setup page</p>} />
            <Route path="/settings" element={<p>settings page</p>} />
          </Routes>
        </MemoryRouter>
      </AuthProvider>,
    )
    expect(await screen.findByText('setup page')).toBeInTheDocument()
  })

  // Audit 2026-10 #6: the check used to be a bare spinner that said nothing.
  it('says what it is doing while it asks the install whether setup is done', async () => {
    renderLogin()
    expect(screen.getByRole('status', { name: 'Checking this install' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('says a refused sign-in failed, with the reason, and keeps the form', async () => {
    renderLogin()
    fireEvent.change(await screen.findByLabelText('Email'), { target: { value: 'a@b.c' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'wrong-pass' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't sign in")
    expect(alert).toHaveTextContent('Invalid email or password')
    expect(screen.getByLabelText('Email')).toHaveValue('a@b.c')
  })
})
