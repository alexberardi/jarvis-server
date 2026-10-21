import { type FormEvent, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import AuthScreen from '@/components/auth/AuthScreen'
import { Alert, Button, Input } from '@/components/ui'

/**
 * ChangePasswordPage is where a user holding a temporary password lands after login (O4):
 * the app is closed to them until they pick their own. Also reachable voluntarily.
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
      setError('The new password needs at least 8 characters.')
      return
    }
    if (next === current) {
      setError('Choose a new password that differs from the current one.')
      return
    }
    setSaving(true)
    try {
      await changePassword(current, next)
      navigate('/', { replace: true })
    } catch (err) {
      setError(errorMessage(err, 'Could not change the password'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <AuthScreen
      title="Change password"
      description={
        mustChangePassword
          ? 'You signed in with a temporary password. Choose your own to continue.'
          : 'Signing in elsewhere will need the new password.'
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          id="current-password"
          label={mustChangePassword ? 'Temporary password' : 'Current password'}
          type="password"
          required
          autoComplete="current-password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
        />
        <Input
          id="new-password"
          label="New password"
          hint="At least 8 characters."
          type="password"
          required
          minLength={8}
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
        <Input
          id="confirm-new-password"
          label="Confirm new password"
          type="password"
          required
          minLength={8}
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />

        {error && <Alert tone="danger">{error}</Alert>}

        <div className="space-y-2 pt-2">
          <Button type="submit" variant="primary" loading={saving} className="w-full">
            Change password
          </Button>
          <Button variant="ghost" onClick={() => (mustChangePassword ? logout() : navigate(-1))} className="w-full">
            {mustChangePassword ? 'Sign out' : 'Cancel'}
          </Button>
        </div>
      </form>
    </AuthScreen>
  )
}
