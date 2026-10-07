import { Download, X } from 'lucide-react'
import { toast } from 'sonner'
import { isActiveInstall, type Install } from '@/api/llm'
import { useCancelInstall, useInstalls } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { installNeedsToken } from './logic'
import { buttonClass, stateTone } from './styles'
import { Pill, ProgressBar, Section } from './ui'

const phaseLabel: Record<string, string> = {
  engine: 'Fetching engine build',
  mmproj: 'Downloading vision projector',
  model: 'Downloading model',
  done: 'Done',
}

/** How far back finished installs stay listed. */
const RECENT_MS = 30 * 60 * 1000

function recent(i: Install): boolean {
  if (isActiveInstall(i)) return true
  const t = Date.parse(i.updated_at)
  return Number.isNaN(t) || Date.now() - t < RECENT_MS
}

export function InstallRow({ install }: { install: Install }) {
  const cancel = useCancelInstall()
  const active = isActiveInstall(install)
  const label = `${install.model_id}${install.mmproj_id ? ` + ${install.mmproj_id}` : ''}`
  return (
    <li className="space-y-1.5 rounded-lg border border-[var(--color-border)] p-3" data-testid={`install-${install.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <span className="font-mono text-sm text-[var(--color-text)]">{label}</span>
          {install.assign && install.assign.length > 0 && (
            <span className="ml-2 text-xs text-[var(--color-text-muted)]">→ {install.assign.join(', ')}</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Pill tone={stateTone(install.state)}>{install.state}</Pill>
          {active && (
            <button
              type="button"
              title="Cancel install"
              aria-label={`Cancel install of ${install.model_id}`}
              className={buttonClass.icon}
              disabled={cancel.isPending}
              onClick={() =>
                cancel.mutate(install.id, {
                  onSuccess: () => toast.success(`Cancelled ${install.model_id}`),
                  onError: (err) => toast.error(errorMessage(err, 'Cancel failed')),
                })
              }
            >
              <X size={14} />
            </button>
          )}
        </div>
      </div>
      {active && (
        <>
          <ProgressBar value={install.bytes_done} max={install.bytes_total} label={`${install.model_id} progress`} />
          <div className="flex justify-between text-xs text-[var(--color-text-muted)]">
            <span>{phaseLabel[install.phase] ?? install.phase}</span>
            <span>
              {formatBytes(install.bytes_done)} / {install.bytes_total ? formatBytes(install.bytes_total) : '…'}
            </span>
          </div>
        </>
      )}
      {install.note && <p className="text-xs text-[var(--color-text-muted)]">{install.note}</p>}
      {install.error && (
        <p className={active ? 'text-xs text-amber-500' : 'text-xs text-red-500'}>
          {active ? `Retrying: ${install.error}` : install.error}
          {installNeedsToken(install) && ' — set a Hugging Face token below and install again.'}
        </p>
      )}
    </li>
  )
}

/** InstallsList shows running and recent installs with progress; it polls while one runs. */
export default function InstallsList({ hideWhenEmpty = true }: { hideWhenEmpty?: boolean }) {
  const { data } = useInstalls()
  const shown = (data ?? []).filter(recent)
  if (shown.length === 0 && hideWhenEmpty) return null
  return (
    <Section title="Installs" icon={Download} description="Downloads resume after a restart and are checked before use.">
      {shown.length === 0 ? (
        <p className="text-sm text-[var(--color-text-muted)]">No installs running.</p>
      ) : (
        <ul className="space-y-2">
          {shown.map((i) => (
            <InstallRow key={i.id} install={i} />
          ))}
        </ul>
      )}
    </Section>
  )
}
