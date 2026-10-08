import { useState } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { setupKeys, useSetupState } from '@/hooks/useSetup'
import { updateSetting } from '@/api/settings'
import AccountStep from '@/components/wizard/AccountStep'
import CheckStep from '@/components/wizard/CheckStep'
import DoneStep from '@/components/wizard/DoneStep'
import HardwareStep from '@/components/wizard/HardwareStep'
import JobStep from '@/components/wizard/JobStep'
import PrivacyStep from '@/components/wizard/PrivacyStep'
import { buttonClass } from '@/components/models/styles'
import { jobDef } from '@/components/setup/jobs'
import {
  JOB_STEPS,
  STEPS,
  TITLES,
  idx,
  initialStep,
  isResumable,
  saveStep,
  serverStep,
  type Initial,
  type Step,
} from '@/components/wizard/steps'

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

/** First-run setup on jarvisd (S3, AQ3/AD3, AD3a, AD3b). */
export default function SetupWizard({ needsSuperuser }: { needsSuperuser: boolean }) {
  const { state } = useAuth()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [initial, setStepState] = useState<Initial>(() => initialStep(needsSuperuser, state.isAuthenticated))
  // A new tab or browser: where the install is, from the server (A10 F9).
  const server = useSetupState(initial === 'server')
  const d = server.data
  let step: Step | null | 'wait' = initial === 'server' ? 'wait' : initial
  if (initial === 'server') {
    if (d?.superuser) step = serverStep(d.setup_step)
    else if (server.isError || (d && !server.isFetching)) step = null
    // Decided once: a later refetch (an install finishing) must not move the wizard.
    if (step !== 'wait') setStepState(step)
  }

  const go = (s: Step | null) => {
    saveStep(s)
    setStepState(s)
    // Any tab or browser resumes here (AD3b: the job steps can't all be derived from the install).
    if (isResumable(s)) updateSetting('admin', 'setup.step', s).catch(() => {})
    // Reaching Done finishes setup for every tab and browser: sign-ins stop resuming it.
    if (s === 'done') updateSetting('admin', 'setup.completed', true).catch(() => {})
    // Each step reads the install as it is now (labels change as installs finish).
    void qc.invalidateQueries({ queryKey: setupKeys.state })
  }

  if (step === 'wait') {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-[var(--color-primary)] border-t-transparent" />
      </div>
    )
  }
  // No `state.isLoading` gate: the session is restored synchronously, so isLoading is only
  // ever true during the setup call itself, and unmounting then wiped the Account form and
  // the reason a refused setup gives (A10). AccountStep shows its own "Creating...".
  if (!step) return <Navigate to={state.isAuthenticated ? '/dashboard' : '/login'} replace />
  // Past Account without a session (it expired, or was signed out): sign in first.
  if (idx(step) > idx('account') && !state.isAuthenticated) return <Navigate to="/login" replace />

  const next = () => go(STEPS[Math.min(idx(step) + 1, STEPS.length - 1)])
  const back = () => go(STEPS[Math.max(idx(step) - 1, idx('hardware'))])

  function finish() {
    go(null)
    navigate('/dashboard', { replace: true })
  }

  const wide = JOB_STEPS.includes(step)
  const job = jobDef(step)

  return (
    <div className="flex min-h-screen justify-center bg-[var(--color-background)] p-4 sm:items-center">
      <div className={cn('w-full', wide ? 'max-w-3xl' : 'max-w-2xl')}>
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
          {job && (
            <JobStep
              key={job.id}
              def={job}
              onNext={next}
              onBack={back}
              // "Install everything recommended" confirms all five job steps at once.
              onAllConfirmed={job.id === 'llm' ? () => go('privacy') : undefined}
            />
          )}
          {step === 'privacy' && <PrivacyStep onDone={next} />}
          {step === 'done' && <DoneStep onFinish={finish} />}
        </div>
      </div>
    </div>
  )
}
