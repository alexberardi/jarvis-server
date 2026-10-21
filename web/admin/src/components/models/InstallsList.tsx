import { Download, X } from 'lucide-react'
import { toast } from 'sonner'
import { isActiveInstall, type Install } from '@/api/llm'
import { useCancelInstall, useInstalls } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { installNeedsToken } from './logic'
import { Badge, Button } from '@/components/ui'
import ModelCard from './ModelCard'
import { stateBadge } from './tone'
import { ProgressBar } from './ui'

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
    <li className="space-y-1.5 py-3 first:pt-0 last:pb-0" data-testid={`install-${install.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <span className="font-mono text-small break-all text-text">{label}</span>
          {install.assign && install.assign.length > 0 && (
            <span className="ml-2 text-small text-text-muted">→ {install.assign.join(', ')}</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={stateBadge(install.state)}>{install.state}</Badge>
          {active && (
            <Button
              variant="ghost"
              size="sm"
              className="px-2"
              title="Cancel install"
              aria-label={`Cancel install of ${install.model_id}`}
              icon={<X size={14} aria-hidden />}
              disabled={cancel.isPending}
              onClick={() =>
                cancel.mutate(install.id, {
                  onSuccess: () => toast.success(`Cancelled ${install.model_id}`),
                  onError: (err) => toast.error(errorMessage(err, 'Cancel failed')),
                })
              }
            >
              Cancel
            </Button>
          )}
        </div>
      </div>
      {active && (
        <>
          <ProgressBar value={install.bytes_done} max={install.bytes_total} label={`${install.model_id} progress`} />
          <div className="flex justify-between gap-2 text-small text-text-muted">
            <span>{phaseLabel[install.phase] ?? install.phase}</span>
            <span>
              {formatBytes(install.bytes_done)} / {install.bytes_total ? formatBytes(install.bytes_total) : '…'}
            </span>
          </div>
        </>
      )}
      {install.note && <p className="text-small text-text-muted">{install.note}</p>}
      {install.error && (
        <p className={active ? 'text-small text-warning' : 'text-small text-danger'}>
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
    <ModelCard title="Installs" icon={Download} description="Downloads resume after a restart and are checked before use.">
      {shown.length === 0 ? (
        <p className="text-small text-text-muted">No installs running.</p>
      ) : (
        <ul className="divide-y divide-border">
          {shown.map((i) => (
            <InstallRow key={i.id} install={i} />
          ))}
        </ul>
      )}
    </ModelCard>
  )
}
