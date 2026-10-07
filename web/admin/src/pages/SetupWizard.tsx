import { useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { setupKeys } from '@/hooks/useSetup'
import AccountStep from '@/components/wizard/AccountStep'
import CheckStep from '@/components/wizard/CheckStep'
import DoneStep from '@/components/wizard/DoneStep'
import HardwareStep from '@/components/wizard/HardwareStep'
import ModelsStep from '@/components/wizard/ModelsStep'
import PrivacyStep from '@/components/wizard/PrivacyStep'
import { buttonClass } from '@/components/models/styles'
import { STEPS, TITLES, idx, initialStep, saveStep, type Step } from '@/components/wizard/steps'

function Stepper({ step, onPick }: { step: Step; onPick: (s: Step) => void }) {
  return (
    <ol className="mb-4 flex flex-wrap items-center gap-1 text-xs" aria-label="Setup steps">
      {STEPS.map((s, i) => {
        const done = i < idx(step)
        const current = s === step
        // Steps from Hardware on can be revisited; Check and Account are behind us for good.
        const canPick = done && idx(s) >= idx('hardware')
        return (
          <li key={s} className="flex items-center gap-1">
            {i > 0 && <span className="mx-1 h-px w-4 bg-[var(--color-border)]" aria-hidden />}
            <button
              type="button"
              disabled={!canPick}
              onClick={() => onPick(s)}
              aria-current={current ? 'step' : undefined}
              className={cn(
                'flex items-center gap-1 rounded-full px-2.5 py-1',
                current && 'bg-[var(--color-primary)] text-white',
                done && 'text-[var(--color-text)]',
                !done && !current && 'text-[var(--color-text-muted)]',
                canPick && 'hover:bg-[var(--color-surface-alt)]',
              )}
            >
              {done && <Check size={12} className="text-green-500" />}
              {TITLES[s]}
            </button>
          </li>
        )
      })}
    </ol>
  )
}

/** First-run setup on jarvisd (S3, AQ3/AD3, AD3a). */
export default function SetupWizard({ needsSuperuser }: { needsSuperuser: boolean }) {
  const { state } = useAuth()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStepState] = useState<Step | null>(() => initialStep(needsSuperuser, state.isAuthenticated))

  const go = (s: Step | null) => {
    saveStep(s)
    setStepState(s)
    // Each step reads the install as it is now (labels change as installs finish).
    void qc.invalidateQueries({ queryKey: setupKeys.state })
  }

  if (state.isLoading) return null
  if (!step) return <Navigate to={state.isAuthenticated ? '/dashboard' : '/login'} replace />
  // Past Account without a session (it expired, or was signed out): sign in first.
  if (idx(step) > idx('account') && !state.isAuthenticated) return <Navigate to="/login" replace />

  const next = () => go(STEPS[Math.min(idx(step) + 1, STEPS.length - 1)])

  function finish() {
    go(null)
    navigate('/dashboard', { replace: true })
  }

  const wide = step === 'models'

  return (
    <div className="flex min-h-screen justify-center bg-[var(--color-background)] p-4 sm:items-center">
      <div className={cn('w-full', wide ? 'max-w-4xl' : 'max-w-2xl')}>
        <div className="mb-2 text-center text-lg font-bold text-[var(--color-primary)]">Jarvis setup</div>
        <Stepper step={step} onPick={go} />
        <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-lg">
          {step === 'check' && (
            <>
              <CheckStep />
              <div className="mt-4 flex justify-end">
                <button type="button" className={buttonClass.primary} onClick={next}>
                  Continue
                </button>
              </div>
            </>
          )}
          {step === 'account' && (
            <AccountStep
              // The session now exists: go() refetches the superuser view of /api/setup/state.
              onCreated={() => go('hardware')}
            />
          )}
          {step === 'hardware' && <HardwareStep onDone={next} />}
          {step === 'models' && <ModelsStep onDone={next} />}
          {step === 'privacy' && <PrivacyStep onDone={next} />}
          {step === 'done' && <DoneStep onFinish={finish} />}
        </div>
      </div>
    </div>
  )
}
