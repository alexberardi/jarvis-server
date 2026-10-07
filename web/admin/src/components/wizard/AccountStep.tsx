import { useEffect, useState } from 'react'
import { CheckCircle2, AlertCircle, KeyRound } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { getSetupState } from '@/api/auth'
import { clearSetupToken, getSetupToken, setSetupToken } from '@/auth/setupToken'

interface AccountStepProps {
  /** Called once the superuser exists and the session is stored. */
  onCreated?: () => void
}

/**
 * AccountStep creates the first superuser through AuthContext.setup (O5: one session store).
 * jarvisd requires the one-time setup token (AD2). It normally arrives in the link jarvisd
 * prints (`/setup#token=…`); when it is missing or rejected, the operator pastes it from the
 * file `/api/setup/state` names.
 */
export default function AccountStep({ onCreated }: AccountStepProps) {
  const { setup, state } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tokenFile, setTokenFile] = useState<string | null>(null)
  const [needToken, setNeedToken] = useState(false)
  const [pastedToken, setPastedToken] = useState('')

  // Ask for the token up front when the link didn't carry one.
  useEffect(() => {
    let cancelled = false
    getSetupState()
      .then((s) => {
        if (cancelled) return
        setTokenFile(s.setup_token_file ?? null)
        if (s.needs_superuser && s.setup_token_required && !getSetupToken()) setNeedToken(true)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  const passwordsMatch = password === confirmPassword
  const longEnough = password.length >= 8
  const tokenReady = !needToken || pastedToken.trim().length > 0
  const canSubmit =
    email && password && confirmPassword && displayName && passwordsMatch && longEnough && tokenReady && !creating

  async function handleCreate() {
    if (!canSubmit) return
    setCreating(true)
    setError(null)
    if (needToken) setSetupToken(pastedToken)

    const res = await setup(email, password, displayName)
    setCreating(false)
    if (res.ok) {
      setCreated(true)
      onCreated?.()
      return
    }
    if (res.tokenProblem) {
      clearSetupToken()
      setNeedToken(true)
      setPastedToken('')
      setError(
        res.status === 403
          ? 'That setup token was not accepted. Paste the current one from the setup-token file.'
          : 'This install needs its setup token. Paste it from the setup-token file.',
      )
      return
    }
    setError(res.message)
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
            <p className="text-sm font-medium text-[var(--color-text)]">Superuser account created</p>
            <p className="text-xs text-[var(--color-text-muted)]">{state.user?.email ?? email}</p>
          </div>
        </div>
        <p className="text-sm text-[var(--color-text-muted)]">You're signed in.</p>
      </div>
    )
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
    'text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
    'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  return (
    <form
      className="space-y-6"
      onSubmit={(e) => {
        e.preventDefault()
        void handleCreate()
      }}
    >
      <div>
        <h2 className="text-xl font-bold text-[var(--color-text)]">Create Account</h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Create your superuser account. This will be the admin for your Jarvis instance.
        </p>
      </div>

      {needToken && (
        <div className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
          <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-text)]">
            <KeyRound size={14} className="text-amber-500" />
            Setup token
          </div>
          <p className="text-xs text-[var(--color-text-muted)]">
            jarvisd printed a setup link when it started. If you opened this page another way, copy the
            token from{' '}
            {tokenFile ? (
              <code className="break-all font-mono text-[var(--color-text)]">{tokenFile}</code>
            ) : (
              <>the <code className="font-mono">setup-token</code> file in jarvisd's home directory</>
            )}{' '}
            on the server and paste it here.
          </p>
          <label htmlFor="setup-token" className="sr-only">
            Setup token
          </label>
          <input
            id="setup-token"
            type="password"
            autoComplete="off"
            value={pastedToken}
            onChange={(e) => setPastedToken(e.target.value)}
            className={cn(inputClass, 'font-mono')}
            placeholder="Paste the setup token"
          />
        </div>
      )}

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
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={inputClass}
            placeholder="At least 8 characters"
          />
        </div>

        <div>
          <label htmlFor="confirm-password" className="mb-1 block text-sm text-[var(--color-text-muted)]">
            Confirm Password
          </label>
          <input
            id="confirm-password"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            className={cn(inputClass, confirmPassword && !passwordsMatch && 'ring-2 ring-red-500')}
            placeholder="Confirm password"
          />
          {confirmPassword && !passwordsMatch && (
            <p className="mt-1 text-xs text-red-500">Passwords don't match</p>
          )}
          {password && !longEnough && (
            <p className="mt-1 text-xs text-[var(--color-text-muted)]">At least 8 characters</p>
          )}
        </div>
      </div>

      {error && (
        <div role="alert" className="flex items-center gap-2 rounded-lg bg-red-500/10 px-3 py-2">
          <AlertCircle size={14} className="shrink-0 text-red-500" />
          <span className="text-sm text-red-500">{error}</span>
        </div>
      )}

      <button
        type="submit"
        disabled={!canSubmit}
        className={cn(
          'w-full rounded-lg bg-[var(--color-primary)] px-4 py-2 font-medium text-white',
          'transition-opacity hover:opacity-90 disabled:opacity-50',
        )}
      >
        {creating ? 'Creating...' : 'Create Superuser Account'}
      </button>
    </form>
  )
}
