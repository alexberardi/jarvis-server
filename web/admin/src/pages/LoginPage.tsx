import { type FormEvent, useEffect, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { getSetupStatus } from '@/api/auth'
import { apiClient } from '@/api/client'
import { cn } from '@/lib/utils'

type Mode = 'loading' | 'setup' | 'login'

export default function LoginPage() {
  const { state, login, setup } = useAuth()
  const navigate = useNavigate()
  const [mode, setMode] = useState<Mode>('loading')
  const [email, setEmail] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    async function checkSetup() {
      // First check if the backend has service URLs configured
      try {
        const { data } = await apiClient.get<{ configured: boolean }>('/api/setup/status')
        if (!cancelled && !data.configured) {
          navigate('/setup', { replace: true })
          return
        }
      } catch {
        // Backend unreachable — redirect to setup wizard
        if (!cancelled) {
          navigate('/setup', { replace: true })
          return
        }
      }

      // URLs are configured — check if we need first-user setup
      try {
        const res = await getSetupStatus()
        if (!cancelled) setMode(res.needs_setup ? 'setup' : 'login')
      } catch {
        if (!cancelled) setMode('login')
      }
    }

    checkSetup()
    return () => { cancelled = true }
  }, [navigate])

  if (state.isAuthenticated) {
    return <Navigate to="/settings" replace />
  }

  const handleLogin = (e: FormEvent) => {
    e.preventDefault()
    login(email, password)
  }

  const handleSetup = (e: FormEvent) => {
    e.preventDefault()
    setValidationError(null)

    if (password !== confirmPassword) {
      setValidationError('Passwords do not match')
      return
    }
    if (password.length < 8) {
      setValidationError('Password must be at least 8 characters')
      return
    }

    setup(email, password, username || undefined)
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
    'text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
    'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  const displayError = validationError ?? state.error

  if (mode === 'loading') {
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
        {mode === 'setup' ? (
          <>
            <h1 className="mb-1 text-center text-2xl font-bold text-[var(--color-text)]">
              Welcome to Jarvis
            </h1>
            <p className="mb-6 text-center text-sm text-[var(--color-text-muted)]">
              Set up your first administrator account to get started.
            </p>

            <form onSubmit={handleSetup} className="space-y-4">
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
                <label htmlFor="username" className="mb-1 block text-sm text-[var(--color-text-muted)]">
                  Username
                </label>
                <input
                  id="username"
                  type="text"
                  required
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className={inputClass}
                  placeholder="admin"
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
                  minLength={8}
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className={inputClass}
                />
              </div>

              <div>
                <label htmlFor="confirm-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
                  Confirm Password
                </label>
                <input
                  id="confirm-password"
                  type="password"
                  required
                  minLength={8}
                  autoComplete="new-password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  className={inputClass}
                />
              </div>

              {displayError && (
                <p className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
                  {displayError}
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
                {state.isLoading ? 'Creating account...' : 'Create Admin Account'}
              </button>
            </form>
          </>
        ) : (
          <>
            <h1 className="mb-6 text-center text-2xl font-bold text-[var(--color-text)]">
              Jarvis Admin
            </h1>

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

              {displayError && (
                <p className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
                  {displayError}
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
          </>
        )}
      </div>
    </div>
  )
}
