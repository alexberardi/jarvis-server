import { type FormEvent, useEffect, useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { getSetupStatus } from '@/api/auth'
import AuthScreen, { AuthLoading } from '@/components/auth/AuthScreen'
import { Alert, Button, Input } from '@/components/ui'

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
    // A superuser goes through /setup, which resumes an unfinished wizard (A10 F9) and
    // otherwise forwards home; a member has no setup to resume.
    const next = mustChangePassword ? '/change-password' : state.user?.is_superuser ? '/setup' : '/'
    return <Navigate to={next} replace />
  }

  const handleLogin = (e: FormEvent) => {
    e.preventDefault()
    login(email, password)
  }

  if (checking) return <AuthLoading label="Checking this install" />

  return (
    <AuthScreen title="Sign in">
      <form onSubmit={handleLogin} className="space-y-4">
        <Input
          id="email"
          label="Email"
          type="email"
          required
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <Input
          id="password"
          label="Password"
          type="password"
          required
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />

        {state.error && (
          <Alert tone="danger" title="Couldn't sign in">
            {state.error}
          </Alert>
        )}

        <Button type="submit" variant="primary" loading={state.isLoading} className="w-full">
          Sign in
        </Button>
      </form>
    </AuthScreen>
  )
}
