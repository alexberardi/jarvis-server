import type { Fit, Resident } from '@/api/llm'
import { formatMB } from '@/lib/format'
import { fitSummary } from './logic'
import { Badge } from '@/components/ui'
import type { Tone } from './styles'
import { badgeTone } from './tone'

const verdicts: Record<string, { label: string; tone: Tone }> = {
  fits: { label: 'Fits', tone: 'ok' },
  tight: { label: 'Tight', tone: 'warn' },
  split: { label: 'Needs 2+ GPUs', tone: 'warn' },
  too_big: { label: 'Too big', tone: 'bad' },
  cpu: { label: 'Runs on CPU', tone: 'muted' },
  in_binary: { label: 'Built in', tone: 'muted' },
}

export default function FitBadge({ fit }: { fit: Fit }) {
  const v = verdicts[fit.verdict] ?? { label: fit.verdict, tone: 'muted' as Tone }
  return (
    <Badge tone={badgeTone[v.tone]} title={fitSummary(fit)}>
      {v.label}
    </Badge>
  )
}

/** ResidentsNote says what is already on the cards, which every verdict is judged against. */
export function ResidentsNote({ residents }: { residents: Resident[] | null | undefined }) {
  if (!residents || residents.length === 0) return null
  return (
    <div className="rounded-control bg-surface-alt px-3 py-2 text-small text-text-muted">
      <span className="font-medium text-text">Already on the GPU: </span>
      {residents
        .map((r) => {
          const who = r.labels.join(' + ')
          const what = r.model ? ` (${r.model})` : ''
          const where = r.devices?.length ? ` on GPU ${r.devices.join(',')}` : ''
          return `${who}${what} ${formatMB(r.needed_mb)}${where}`
        })
        .join('; ')}
      . Fit verdicts count these.
    </div>
  )
}
