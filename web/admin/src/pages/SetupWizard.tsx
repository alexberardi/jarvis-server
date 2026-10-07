import { useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import AccountStep from '@/components/wizard/AccountStep'

/**
 * First-run setup on jarvisd.
 *
 * Interim (A5): only the Account step is live; the Docker-era Welcome/Services/Review/Install
 * steps have no backend under jarvisd. A7 rebuilds this as Check → Account → Hardware → Models →
 * Privacy → Done (AD3, AD3a), reusing the Models page components (A6).
 */
export default function SetupWizard({ needsSuperuser }: { needsSuperuser: boolean }) {
  const { state } = useAuth()
  const navigate = useNavigate()
  const [accountDone, setAccountDone] = useState(false)

  // Setup was already done before this page loaded: there is nothing to do here.
  if (!needsSuperuser && !accountDone) {
    return <Navigate to={state.isAuthenticated ? '/dashboard' : '/login'} replace />
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)] p-4">
      <div className="w-full max-w-2xl">
        <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-lg">
          <AccountStep onCreated={() => setAccountDone(true)} />
        </div>
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            onClick={() => navigate('/models')}
            disabled={!state.isAuthenticated}
            className={cn(
              'rounded-lg bg-[var(--color-primary)] px-6 py-2 text-sm font-medium text-white',
              'transition-opacity hover:opacity-90',
              'disabled:cursor-not-allowed disabled:opacity-50',
            )}
          >
            Next: models
          </button>
        </div>
      </div>
    </div>
  )
}
