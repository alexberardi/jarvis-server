import { CheckCircle2, PartyPopper, RefreshCw, Smartphone } from 'lucide-react'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import { Pill } from '@/components/models/ui'
import { buttonClass, stateTone } from '@/components/models/styles'
import { LABEL_TITLE, type Label } from '@/api/llm'
import { useDoctor, useRerunDoctor, useSetupState } from '@/hooks/useSetup'
import { useSystemInfo } from '@/hooks/useSystem'
import { cn } from '@/lib/utils'

const SHOWN_LABELS: Label[] = ['live', 'background', 'embeddings', 'stt', 'tts', 'speaker']

const STATE_TEXT: Record<string, string> = {
  not_configured: 'not configured',
  fetching_engine: 'fetching engine',
  no_engine_build: 'no engine build',
}

/**
 * DoneStep sums up: what each model job runs (and whether it is ready), the prompt provider,
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

  return (
    <div className="space-y-5">
      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <PartyPopper size={20} /> Jarvis is set up
        </h2>
        {s && !s.models_configured && (
          <p className="mt-1 text-sm text-amber-500">
            No language model is assigned yet, so voice requests can't be answered. Install one from the Models page; the
            dashboard will remind you.
          </p>
        )}
        {s?.models_configured && !s.live_ready && (
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            The live model is still loading. It will be ready in a moment; the dashboard shows its progress.
          </p>
        )}
      </div>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold text-[var(--color-text)]">Models</h3>
        <ul className="grid gap-1 sm:grid-cols-2">
          {SHOWN_LABELS.map((l) => {
            const st = s?.labels?.[l] ?? 'unknown'
            return (
              <li key={l} className="flex items-center justify-between gap-2 text-sm">
                <span className="text-[var(--color-text)]">{LABEL_TITLE[l]}</span>
                <Pill tone={stateTone(st)}>{STATE_TEXT[st] ?? st}</Pill>
              </li>
            )
          })}
        </ul>
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
