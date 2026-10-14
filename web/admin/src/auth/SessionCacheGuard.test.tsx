import { describe, expect, it } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider, useAuth } from '@/auth/AuthContext'
import SessionCacheGuard from './SessionCacheGuard'

function LogoutButton() {
  const { logout } = useAuth()
  return <button onClick={logout}>out</button>
}

describe('SessionCacheGuard (G13)', () => {
  it("drops the cached queries when the user signs out", async () => {
    localStorage.setItem('jarvis-admin:access_token', 'a.e30.s')
    localStorage.setItem('jarvis-admin:refresh_token', 'r')
    localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', is_superuser: false }))
    const qc = new QueryClient()
    qc.setQueryData(['inbox', 'h1', 'list'], [{ id: 'secret' }])
    render(
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <SessionCacheGuard />
          <LogoutButton />
        </AuthProvider>
      </QueryClientProvider>,
    )
    expect(qc.getQueryData(['inbox', 'h1', 'list'])).toBeDefined()
    await act(async () => {
      fireEvent.click(screen.getByText('out'))
    })
    expect(qc.getQueryData(['inbox', 'h1', 'list'])).toBeUndefined()
  })
})
