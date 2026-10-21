import { useEffect, useState } from 'react'
import { UserRound } from 'lucide-react'
import { Alert, Button, Input } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { getSetupState } from '@/api/auth'
import { clearSetupToken, getSetupToken, setSetupToken } from '@/auth/setupToken'
import { StepActions, StepHeader } from './StepFrame'

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
      <div className="space-y-5">
        <StepHeader icon={<UserRound size={22} />} title="Account created" />
        <Alert tone="success" title="Superuser account created">
          {state.user?.email ?? email}
        </Alert>
        <p className="text-body text-text-muted">You're signed in.</p>
      </div>
    )
  }

  return (
    <form
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault()
        void handleCreate()
      }}
    >
      <StepHeader icon={<UserRound size={22} />} title="Create account">
        The first account is the superuser: it runs this install and adds everyone else.
      </StepHeader>

      <div className="space-y-4">
        {needToken && (
          <Input
            id="setup-token"
            label="Setup token"
            type="password"
            autoComplete="off"
            className="font-mono"
            value={pastedToken}
            onChange={(e) => setPastedToken(e.target.value)}
            placeholder="Paste the setup token"
            hint={
              <>
                jarvisd printed a setup link when it started. If you opened this page another way, copy the token from{' '}
                {tokenFile ? (
                  <code className="font-mono break-all text-text">{tokenFile}</code>
                ) : (
                  <>
                    the <code className="font-mono">setup-token</code> file in jarvisd's home directory
                  </>
                )}{' '}
                on the server and paste it here.
              </>
            }
          />
        )}
        <Input
          id="display-name"
          label="Display name"
          hint="What Jarvis calls you."
          type="text"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder="Alex"
        />
        <Input
          id="email"
          label="Email"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="admin@example.com"
        />
        <Input
          id="password"
          label="Password"
          hint="At least 8 characters."
          error={password && !longEnough && confirmPassword ? 'Use at least 8 characters.' : undefined}
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Input
          id="confirm-password"
          label="Confirm password"
          error={confirmPassword && !passwordsMatch ? "The passwords don't match." : undefined}
          type="password"
          autoComplete="new-password"
          value={confirmPassword}
          onChange={(e) => setConfirmPassword(e.target.value)}
        />
      </div>

      {error && (
        <Alert tone="danger" title="Couldn't create the account">
          {error}
        </Alert>
      )}

      <StepActions>
        <Button type="submit" variant="primary" loading={creating} disabled={!canSubmit}>
          Create superuser account
        </Button>
      </StepActions>
    </form>
  )
}
