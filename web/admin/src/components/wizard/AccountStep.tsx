import { useState } from 'react'
import { CheckCircle2, AlertCircle, RotateCcw } from 'lucide-react'
import { cn } from '@/lib/utils'
import { setup } from '@/api/auth'
import { setAuthToken } from '@/api/client'

export default function AccountStep() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const passwordsMatch = password === confirmPassword
  const canSubmit = email && password && confirmPassword && displayName && passwordsMatch && !creating

  async function handleCreate() {
    if (!canSubmit) return
    setCreating(true)
    setError(null)

    try {
      // Uses /auth/setup — auto-promotes first user to superuser
      const tokens = await setup(email, password, displayName)
      setAuthToken(tokens.access_token)
      localStorage.setItem('access_token', tokens.access_token)
      localStorage.setItem('refresh_token', tokens.refresh_token)

      setCreated(true)
    } catch (err) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? (err instanceof Error ? err.message : 'Failed to create account')
      setError(msg)
    } finally {
      setCreating(false)
    }
  }

  if (created) {
    return (
      <div className="space-y-6">
        <div>
          <h2 className="text-xl font-bold text-[var(--color-text)]">Account Created</h2>
        </div>
        <div className="flex items-center gap-3 rounded-lg border border-green-500/30 bg-green-500/5 p-4">
          <CheckCircle2 size={20} className="text-green-500" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">
              Superuser account created
            </p>
            <p className="text-xs text-[var(--color-text-muted)]">{email}</p>
          </div>
        </div>
        <p className="text-sm text-[var(--color-text-muted)]">
          You're logged in. Click "Next" to configure your LLM model.
        </p>
      </div>
    )
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
    'text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
    'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold text-[var(--color-text)]">Create Account</h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Create your superuser account. This will be the admin for your Jarvis instance.
        </p>
      </div>

      <div className="space-y-4">
        <div>
          <label htmlFor="display-name" className="mb-1 block text-sm text-[var(--color-text-muted)]">
            Display Name
          </label>
          <input
            id="display-name"
            type="text"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className={inputClass}
            placeholder="Alex"
          />
        </div>

        <div>
          <label htmlFor="email" className="mb-1 block text-sm text-[var(--color-text-muted)]">
            Email
          </label>
          <input
            id="email"
            type="email"
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
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={inputClass}
            placeholder="Choose a strong password"
          />
        </div>

        <div>
          <label htmlFor="confirm-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
            Confirm Password
          </label>
          <input
            id="confirm-password"
            type="password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            className={cn(
              inputClass,
              confirmPassword && !passwordsMatch && 'ring-2 ring-red-500',
            )}
            placeholder="Confirm password"
          />
          {confirmPassword && !passwordsMatch && (
            <p className="mt-1 text-xs text-red-500">Passwords don't match</p>
          )}
        </div>
      </div>

      {error && (
        <div className="space-y-2">
          <div className="flex items-center gap-2 rounded-lg bg-red-500/10 px-3 py-2">
            <AlertCircle size={14} className="text-red-500 shrink-0" />
            <span className="text-sm text-red-500">{error}</span>
          </div>
          <button
            type="button"
            onClick={handleCreate}
            disabled={!canSubmit}
            className={cn(
              'flex w-full items-center justify-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm',
              'hover:bg-[var(--color-surface-alt)] transition-colors',
              'disabled:opacity-50',
            )}
          >
            <RotateCcw size={14} />
            Retry
          </button>
        </div>
      )}

      {!error && (
        <button
          type="button"
          onClick={handleCreate}
          disabled={!canSubmit}
          className={cn(
            'w-full rounded-lg bg-[var(--color-primary)] px-4 py-2 font-medium text-white',
            'hover:opacity-90 disabled:opacity-50 transition-opacity',
          )}
        >
          {creating ? 'Creating...' : 'Create Superuser Account'}
        </button>
      )}
    </div>
  )
}
