import { type FormEvent, useEffect, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { getSetupStatus } from '@/api/auth'
import { cn } from '@/lib/utils'

export default function LoginPage() {
  const { state, mustChangePassword, login } = useAuth()
  const navigate = useNavigate()
  const [checking, setChecking] = useState(true)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  // jarvisd has no "service URLs not configured" state (S2): the only reason not to log in is
  // that no superuser exists yet, and then first-run setup (with its token, AD2) is the way in.
  useEffect(() => {
    let cancelled = false
    getSetupStatus()
      .then((res) => {
        if (!cancelled && res.needs_setup) navigate('/setup', { replace: true })
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setChecking(false)
      })
    return () => {
      cancelled = true
    }
  }, [navigate])

  if (state.isAuthenticated) {
    // /setup resumes an unfinished wizard from the server's state (A10 F9) and otherwise
    // forwards to the dashboard.
    return <Navigate to={mustChangePassword ? '/change-password' : '/setup'} replace />
  }

  const handleLogin = (e: FormEvent) => {
    e.preventDefault()
    login(email, password)
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
    'text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
    'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <div className="w-full max-w-sm rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 shadow-lg">
          <div className="flex justify-center">
            <div className="h-8 w-8 animate-spin rounded-full border-4 border-[var(--color-border)] border-t-[var(--color-primary)]" />
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
      <div className="w-full max-w-sm rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 shadow-lg">
        <h1 className="mb-6 text-center text-2xl font-bold text-[var(--color-text)]">Jarvis Admin</h1>

        <form onSubmit={handleLogin} className="space-y-4">
          <div>
            <label htmlFor="email" className="mb-1 block text-sm text-[var(--color-text-muted)]">
              Email
            </label>
            <input
              id="email"
              type="email"
              required
              autoComplete="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className={inputClass}
              placeholder="admin@example.com"
            />
          </div>

          <div>
            <label htmlFor="password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
              Password
            </label>
            <input
              id="password"
              type="password"
              required
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className={inputClass}
            />
          </div>

          {state.error && (
            <p role="alert" className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
              {state.error}
            </p>
          )}

          <button
            type="submit"
            disabled={state.isLoading}
            className={cn(
              'w-full rounded-lg bg-[var(--color-primary)] px-4 py-2 font-medium text-white',
              'hover:opacity-90 disabled:opacity-50',
              'transition-opacity',
            )}
          >
            {state.isLoading ? 'Signing in...' : 'Sign in'}
          </button>
        </form>
      </div>
    </div>
  )
}
