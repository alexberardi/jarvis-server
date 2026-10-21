import { useState } from 'react'
import { Navigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Check, ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/hooks/useAuth'
import { setupKeys, useSetupState } from '@/hooks/useSetup'
import { updateSetting } from '@/api/settings'
import AccountStep from '@/components/wizard/AccountStep'
import CheckStep from '@/components/wizard/CheckStep'
import ComputerStep from '@/components/wizard/ComputerStep'
import DoneStep from '@/components/wizard/DoneStep'
import HardwareStep from '@/components/wizard/HardwareStep'
import JobStep from '@/components/wizard/JobStep'
import PrivacyStep from '@/components/wizard/PrivacyStep'
import { AuthLoading } from '@/components/auth/AuthScreen'
import { Button, Card } from '@/components/ui'
import { StepActions } from '@/components/wizard/StepFrame'
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

/**
 * The step list. From `sm` up it is a row of labelled pills that wraps. Eleven don't fit a
 * phone (audit 2026-10 #7: numbered dots that said nothing), so there the wizard says
 * "Step 6 of 11: Voice" over a progress line, and that line opens the same list, one step per
 * row, for going back to an earlier step.
 */
function Stepper({ step, onPick }: { step: Step; onPick: (s: Step) => void }) {
  const at = idx(step)
  const [open, setOpen] = useState(false)
  const pick = (s: Step) => {
    setOpen(false)
    onPick(s)
  }
  return (
    <nav aria-label="Setup progress" className="mb-4">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-2 rounded-control py-1 text-left text-small text-text-muted sm:hidden"
        aria-expanded={open}
        aria-controls="setup-steps"
        onClick={() => setOpen((o) => !o)}
      >
        <span data-testid="setup-step-position">
          Step {at + 1} of {STEPS.length}: <span className="font-semibold text-text">{TITLES[step]}</span>
        </span>
        <ChevronDown size={16} aria-hidden className={cn('shrink-0 transition-transform duration-(--duration-menu) ease-out motion-reduce:transition-none', open && 'rotate-180')} />
      </button>
      <div aria-hidden className="mt-1 h-1 overflow-hidden rounded-pill bg-surface-alt sm:hidden">
        <div className="h-full rounded-pill bg-primary" style={{ width: `${((at + 1) / STEPS.length) * 100}%` }} />
      </div>
      <ol
        id="setup-steps"
        aria-label="Setup steps"
        className={cn(
          'flex gap-1 text-small max-sm:mt-2 max-sm:flex-col max-sm:rounded-card max-sm:border max-sm:border-border max-sm:bg-surface max-sm:p-1 sm:flex-wrap',
          !open && 'max-sm:hidden',
        )}
      >
        {STEPS.map((s, i) => {
          const done = i < at
          const current = s === step
          // Steps from Hardware on can be revisited; Check and Account are behind us for good.
          const canPick = done && idx(s) >= idx('hardware')
          return (
            <li key={s} className="flex">
              <button
                type="button"
                disabled={!canPick}
                onClick={() => pick(s)}
                aria-current={current ? 'step' : undefined}
                className={cn(
                  'flex min-h-8 items-center gap-1.5 rounded-pill px-3 whitespace-nowrap max-sm:w-full max-sm:min-h-10 max-sm:rounded-control',
                  'transition-colors duration-(--duration-hover)',
                  current && 'bg-primary font-medium text-on-primary',
                  done && 'text-text',
                  !done && !current && 'text-text-muted',
                  canPick && 'hover:bg-surface-alt',
                )}
              >
                {done && <Check size={14} aria-hidden className="text-success" />}
                {TITLES[s]}
              </button>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

/** First-run setup on jarvisd (S3, AQ3/AD3, AD3a, AD3b). */
export default function SetupWizard({ needsSuperuser }: { needsSuperuser: boolean }) {
  const { state } = useAuth()
  const qc = useQueryClient()
  const [initial, setStepState] = useState<Initial>(() => initialStep(needsSuperuser, state.isAuthenticated))
  // Done's button goes to the dashboard; any other way out of the wizard goes home.
  const [finished, setFinished] = useState(false)
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

  if (step === 'wait') return <AuthLoading label="Finding where setup left off" />
  // No `state.isLoading` gate: the session is restored synchronously, so isLoading is only
  // ever true during the setup call itself, and unmounting then wiped the Account form and
  // the reason a refused setup gives (A10). AccountStep's button shows its own progress.
  if (!step) return <Navigate to={finished ? '/admin/dashboard' : state.isAuthenticated ? '/' : '/login'} replace />
  // Past Account without a session (it expired, or was signed out): sign in first.
  if (idx(step) > idx('account') && !state.isAuthenticated) return <Navigate to="/login" replace />

  const next = () => go(STEPS[Math.min(idx(step) + 1, STEPS.length - 1)])
  const back = () => go(STEPS[Math.max(idx(step) - 1, idx('hardware'))])

  function finish() {
    setFinished(true)
    go(null)
  }

  const wide = JOB_STEPS.includes(step)
  const job = jobDef(step)

  return (
    <main className="flex min-h-dvh justify-center bg-background px-4 py-6 sm:items-center sm:py-10">
      <div className={cn('w-full', wide ? 'max-w-3xl' : 'max-w-2xl')}>
        <h1 className="mb-3 text-subheading text-primary">Jarvis Automation setup</h1>
        <Stepper step={step} onPick={go} />
        <Card className="md:p-6">
          {step === 'check' && (
            <>
              <CheckStep />
              <StepActions className="mt-6">
                <Button variant="primary" onClick={next}>
                  Continue
                </Button>
              </StepActions>
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
          {step === 'computer' && <ComputerStep onNext={next} onBack={back} />}
          {step === 'done' && <DoneStep onFinish={finish} />}
        </Card>
      </div>
    </main>
  )
}
