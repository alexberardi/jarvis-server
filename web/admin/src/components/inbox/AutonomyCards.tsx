import { CheckCircle2, Clock, ExternalLink, SkipForward, XCircle } from 'lucide-react'
import Markdown from '@/components/chat/Markdown'
import { Badge, Card, type BadgeTone } from '@/components/ui'
import type { RanCardData, RanStepStatus, WatchAlertData } from '@/lib/inbox/autonomyCards'
import { formatServerTime } from '@/lib/time'

// Wording as on mobile (jarvis-node-mobile src/components/inbox/), so both apps read the same.
const RAN_STATES: Record<string, { label: string; tone: BadgeTone }> = {
  running: { label: 'Running…', tone: 'primary' },
  waiting: { label: 'Waiting for your OK', tone: 'warning' },
  done: { label: 'Done', tone: 'success' },
  partial: { label: 'Finished with issues', tone: 'warning' },
  failed: { label: "Couldn't finish", tone: 'danger' },
  stopped: { label: 'Stopped', tone: 'neutral' },
}

const SCOPES: Record<string, string> = {
  household: 'Household rule',
  watch: 'Watch rule',
  goal: 'Goal rule',
  automation: 'Set on the automation',
  tool: "The tool says it's safe to run without asking",
}

const STEP_ICON: Record<RanStepStatus, { icon: typeof CheckCircle2; className: string; label: string }> = {
  ok: { icon: CheckCircle2, className: 'text-success', label: 'Done' },
  failed: { icon: XCircle, className: 'text-danger', label: 'Failed' },
  skipped: { icon: SkipForward, className: 'text-text-muted', label: 'Skipped' },
  pending: { icon: Clock, className: 'text-text-muted', label: 'Not run yet' },
}

/** RanCard is the "Ran on its own" card (card_type autonomy_ran): the answer, each step, and why it was allowed. */
export function RanCard({ data }: { data: RanCardData }) {
  const state = RAN_STATES[data.state]
  return (
    <Card as="section" aria-label="What Jarvis ran" className="space-y-4" data-testid="ran-card">
      <p className="flex flex-wrap items-center gap-2 text-small text-text-muted">
        {state && <Badge tone={state.tone}>{state.label}</Badge>}
        <span>{data.source.kind === 'automation' ? `From: Automation · ${data.source.label}` : 'From: Errand'}</span>
      </p>
      {data.answer && (
        <div className="text-body text-text">
          <Markdown>{data.answer}</Markdown>
        </div>
      )}
      {data.steps.length > 0 && (
        <ol className="space-y-2.5">
          {data.steps.map((s, i) => {
            const { icon: Icon, className, label } = STEP_ICON[s.status]
            return (
              <li key={i} className="flex gap-2.5">
                <Icon size={18} className={`mt-0.5 shrink-0 ${className}`} aria-label={label} />
                <div className="min-w-0">
                  <p className="text-body text-text [overflow-wrap:anywhere]">{s.label || s.command}</p>
                  {s.result && <p className="text-small text-text-muted [overflow-wrap:anywhere]">{s.result}</p>}
                </div>
              </li>
            )
          })}
        </ol>
      )}
      {data.rules.length > 0 && (
        <div className="space-y-2.5 border-t border-border pt-4">
          <h2 className="text-subheading text-text">Why it was allowed</h2>
          <ul className="space-y-2">
            {data.rules.map((r, i) => (
              <li key={i} className="text-small">
                <p className="font-medium text-text">{SCOPES[r.scope] ?? r.scope}</p>
                <p className="text-text-muted">{r.tool}</p>
                {r.note && <p className="text-text-muted italic">“{r.note}”</p>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  )
}

const WATCH_STATES: Record<string, string> = { done: 'Stopped', paused: 'Paused' }

/** WatchAlertCard is a fired watch (card_type watch_alert): what it watches for, the change, and its page. */
export function WatchAlertCard({ data }: { data: WatchAlertData }) {
  const state = WATCH_STATES[data.state]
  const change = data.previous && data.previous !== data.value ? `${data.previous} → ${data.value}` : data.value
  return (
    <Card as="section" aria-label="Watch" className="space-y-3" data-testid="watch-card">
      {state && <Badge>{state}</Badge>}
      <div>
        <p className="text-small text-text-muted">Watching for</p>
        <p className="text-body text-text [overflow-wrap:anywhere]">{data.lookingFor}</p>
      </div>
      <p className="text-title text-text tabular-nums [overflow-wrap:anywhere]">{change}</p>
      {data.checkedAt && <p className="text-small text-text-muted">Checked {formatServerTime(data.checkedAt)}</p>}
      {data.sourceUrl && /^https?:\/\//i.test(data.sourceUrl) && (
        <a
          href={data.sourceUrl}
          target="_blank"
          rel="noopener noreferrer nofollow"
          className="inline-flex min-h-10 items-center gap-1.5 text-small font-medium text-primary underline-offset-2 hover:underline"
        >
          Open {data.sourceLabel || 'link'} <ExternalLink size={14} aria-hidden />
        </a>
      )}
    </Card>
  )
}
