import { CheckCircle2, PartyPopper, RefreshCw, Smartphone } from 'lucide-react'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import JobChecklist from '@/components/setup/JobChecklist'
import { joinTitles, missingRequired } from '@/components/setup/jobs'
import { Alert, Button } from '@/components/ui'
import { useDoctor, useRerunDoctor, useSetupState } from '@/hooks/useSetup'
import { useSystemInfo } from '@/hooks/useSystem'
import { StepActions, StepHeader, StepLoading } from './StepFrame'

const sectionTitle = 'flex items-center gap-2 text-subheading text-text'

/**
 * DoneStep sums up: the per-job model checklist (AD3b), with what voice still lacks named and
 * finishing still allowed, the prompt provider,
 * the doctor's verdict re-run now, and what to do next: the mobile app, then nodes.
 */
export default function DoneStep({ onFinish }: { onFinish: () => void }) {
  const state = useSetupState()
  const doctor = useDoctor()
  const rerun = useRerunDoctor()
  const sys = useSystemInfo()
  const s = state.data
  const configPort = sys.data?.listeners?.find((l) => l.name === 'config')?.port ?? 7700
  const host = typeof window !== 'undefined' ? window.location.hostname : 'this-machine'
  const missing = missingRequired(s?.jobs)

  return (
    <div className="space-y-6">
      <div className="space-y-4">
        <StepHeader icon={<PartyPopper size={22} />} title="Jarvis is set up" />
        {missing.length > 0 && (
          <Alert tone="warning">
            <p>
              Voice requests won't work until {joinTitles(missing.map((m) => m.title))}{' '}
              {missing.length === 1 ? 'is' : 'are'} installed. You can finish now and add{' '}
              {missing.length === 1 ? 'it' : 'them'} from the Models page; the dashboard will remind you.
            </p>
          </Alert>
        )}
        {missing.length === 0 && s?.jobs?.some((j) => j.state === 'downloading' || j.state === 'loading') && (
          <p className="text-body text-text-muted">
            Some models are still downloading or starting. They carry on in the background; the dashboard shows their
            progress.
          </p>
        )}
      </div>

      <section className="space-y-2" aria-labelledby="done-models">
        <h3 id="done-models" className={sectionTitle}>
          Models
        </h3>
        <JobChecklist jobs={s?.jobs} wizard />
        {s?.prompt_provider && (
          <p className="text-small text-text-muted">
            Prompt provider: <code className="text-text">{s.prompt_provider.effective || 'none'}</code>
            {s.prompt_provider.source === 'model' && ' (from the live model)'}
            {s.prompt_provider.source === 'setting' && ' (set by you)'}
            {!s.prompt_provider.valid && s.models_configured && (
              <span className="text-warning">. Pick one on the Models page, or voice requests will fail.</span>
            )}
          </p>
        )}
      </section>

      <section className="space-y-2" aria-labelledby="done-health">
        <div className="flex items-center justify-between gap-3">
          <h3 id="done-health" className={sectionTitle}>
            Health check
          </h3>
          <Button size="sm" icon={<RefreshCw size={14} aria-hidden />} loading={rerun.isPending} onClick={() => rerun.mutate()}>
            Run again
          </Button>
        </div>
        {doctor.data ? (
          <DoctorChecks checks={doctor.data.checks} onlyProblems />
        ) : doctor.isLoading ? (
          <StepLoading>Running the checks</StepLoading>
        ) : (
          <p className="text-small text-text-muted">The checks could not run. Run again, or run jarvisd doctor on the server.</p>
        )}
      </section>

      <section className="space-y-2" aria-labelledby="done-next">
        <h3 id="done-next" className={sectionTitle}>
          <Smartphone size={16} aria-hidden className="text-text-muted" /> Next steps
        </h3>
        <ol className="list-decimal space-y-1.5 pl-5 text-body text-text marker:text-text-muted">
          <li>
            Install the Jarvis mobile app and sign in with the account you just made. On the same network it finds this
            server by itself; if not, enter <code className="break-all">http://{host}:{configPort}</code>.
          </li>
          <li>Add a voice node from the app: it provisions the node with a QR code.</li>
          <li>Say “Jarvis” to the node and ask something.</li>
        </ol>
      </section>

      <StepActions>
        <Button variant="primary" icon={<CheckCircle2 size={16} aria-hidden />} onClick={onFinish}>
          Go to the dashboard
        </Button>
      </StepActions>
    </div>
  )
}
