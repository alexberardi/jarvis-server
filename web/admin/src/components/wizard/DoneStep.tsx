import { CheckCircle2, PartyPopper, RefreshCw, Smartphone } from 'lucide-react'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import JobChecklist from '@/components/setup/JobChecklist'
import { joinTitles, missingRequired } from '@/components/setup/jobs'
import { buttonClass } from '@/components/models/styles'
import { useDoctor, useRerunDoctor, useSetupState } from '@/hooks/useSetup'
import { useSystemInfo } from '@/hooks/useSystem'
import { cn } from '@/lib/utils'

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
    <div className="space-y-5">
      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <PartyPopper size={20} /> Jarvis is set up
        </h2>
        {missing.length > 0 && (
          <p className="mt-1 text-sm text-amber-500" role="status">
            Voice requests won't work until {joinTitles(missing.map((m) => m.title))}{' '}
            {missing.length === 1 ? 'is' : 'are'} installed. You can finish now and add{' '}
            {missing.length === 1 ? 'it' : 'them'} from the Models page; the dashboard will remind you.
          </p>
        )}
        {missing.length === 0 && s?.jobs?.some((j) => j.state === 'downloading' || j.state === 'loading') && (
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            Some models are still downloading or starting. They carry on in the background; the dashboard shows their
            progress.
          </p>
        )}
      </div>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold text-[var(--color-text)]">Models</h3>
        <JobChecklist jobs={s?.jobs} wizard />
        {s?.prompt_provider && (
          <p className="text-xs text-[var(--color-text-muted)]">
            Prompt provider: <code className="text-[var(--color-text)]">{s.prompt_provider.effective || 'none'}</code>
            {s.prompt_provider.source === 'model' && ' (from the live model)'}
            {s.prompt_provider.source === 'setting' && ' (set by you)'}
            {!s.prompt_provider.valid && s.models_configured && (
              <span className="text-amber-500"> — pick one on the Models page, or voice requests will fail</span>
            )}
          </p>
        )}
      </section>

      <section className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-[var(--color-text)]">Health check</h3>
          <button type="button" className={buttonClass.secondary} disabled={rerun.isPending} onClick={() => rerun.mutate()}>
            <RefreshCw size={12} className={cn(rerun.isPending && 'animate-spin')} /> Run again
          </button>
        </div>
        {doctor.data ? (
          <DoctorChecks checks={doctor.data.checks} onlyProblems />
        ) : (
          <p className="text-xs text-[var(--color-text-muted)]">{doctor.isLoading ? 'Running checks…' : 'Checks unavailable.'}</p>
        )}
      </section>

      <section className="space-y-2 rounded-lg border border-[var(--color-border)] p-3">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
          <Smartphone size={14} /> Next steps
        </h3>
        <ol className="list-decimal space-y-1 pl-5 text-sm text-[var(--color-text)]">
          <li>
            Install the Jarvis mobile app and sign in with the account you just made. On the same network it finds this
            server by itself; if not, enter <code>http://{host}:{configPort}</code>.
          </li>
          <li>Add a voice node from the app: it provisions the node with a QR code.</li>
          <li>Say “Jarvis” to the node and ask something.</li>
        </ol>
      </section>

      <div className="flex justify-end">
        <button type="button" className={buttonClass.primary} onClick={onFinish}>
          <CheckCircle2 size={14} /> Go to the dashboard
        </button>
      </div>
    </div>
  )
}
