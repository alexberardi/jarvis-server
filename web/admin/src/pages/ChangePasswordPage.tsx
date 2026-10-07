import { type FormEvent, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

/**
 * ChangePasswordPage is where a superuser holding a temporary password lands after login (O4):
 * the admin is closed to them until they pick their own. Also reachable voluntarily.
 */
export default function ChangePasswordPage() {
  const { state, mustChangePassword, changePassword, logout } = useAuth()
  const navigate = useNavigate()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!state.isAuthenticated) return <Navigate to="/login" replace />

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (next !== confirm) {
      setError('Passwords do not match')
      return
    }
    if (next.length < 8) {
      setError('Password must be at least 8 characters')
      return
    }
    if (next === current) {
      setError('Choose a password different from the current one')
      return
    }
    setSaving(true)
    try {
      await changePassword(current, next)
      navigate('/settings', { replace: true })
    } catch (err) {
      setError(errorMessage(err, 'Could not change the password'))
    } finally {
      setSaving(false)
    }
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
    'text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
    'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
      <div className="w-full max-w-sm rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 shadow-lg">
        <h1 className="mb-1 text-center text-2xl font-bold text-[var(--color-text)]">Change password</h1>
        <p className="mb-6 text-center text-sm text-[var(--color-text-muted)]">
          {mustChangePassword
            ? 'You signed in with a temporary password. Choose your own to continue.'
            : 'Signing in elsewhere will need the new password.'}
        </p>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="current-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
              {mustChangePassword ? 'Temporary password' : 'Current password'}
            </label>
            <input
              id="current-password"
              type="password"
              required
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="new-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
              New password
            </label>
            <input
              id="new-password"
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="confirm-new-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
              Confirm new password
            </label>
            <input
              id="confirm-new-password"
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              className={inputClass}
            />
          </div>

          {error && (
            <p role="alert" className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
              {error}
            </p>
          )}

          <button
            type="submit"
            disabled={saving}
            className="w-full rounded-lg bg-[var(--color-primary)] px-4 py-2 font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            {saving ? 'Saving...' : 'Change password'}
          </button>
          <button
            type="button"
            onClick={() => (mustChangePassword ? logout() : navigate(-1))}
            className="w-full rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
          >
            {mustChangePassword ? 'Sign out' : 'Cancel'}
          </button>
        </form>
      </div>
    </div>
  )
}
