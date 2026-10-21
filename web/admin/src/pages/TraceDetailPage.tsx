import { useMemo, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ArrowLeft } from 'lucide-react'
import { fetchTrace, type TraceSpan } from '@/api/traces'
import { Button, Card, CardHeader, ErrorState, PageHeader, Skeleton } from '@/components/ui'
import { SourceIcon, TraceStatus } from '@/components/traces/TraceBits'
import { errorMessage, errorStatus } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { formatDuration, sourceLabel } from '@/lib/traces'
import { cn } from '@/lib/utils'

// Each service's colour in the flow and the spans, from the theme's tokens so both themes hold.
const SERVICE_COLORS: Record<string, string> = {
  cc: 'var(--color-border-strong)',
  llm_proxy: 'var(--color-primary)',
  node: 'var(--color-success)',
  tts: 'var(--color-warning)',
  whisper: 'var(--color-tertiary)',
}
const ERROR_COLOR = 'var(--color-danger)'

function spanColor(service: string, status: string): string {
  return status === 'error' ? ERROR_COLOR : (SERVICE_COLORS[service] ?? 'var(--color-border-strong)')
}

const SERVICE_LABELS: Record<string, string> = {
  cc: 'Command Center',
  llm_proxy: 'LLM Proxy',
  node: 'Node',
  tts: 'TTS',
  whisper: 'Whisper',
}

interface ServiceHop {
  service: string
  duration_ms: number
  status: string
  steps: string[]
}

/** Filter raw spans to leaf-only, then merge consecutive same-service spans. */
function buildServiceHops(spans: TraceSpan[]): ServiceHop[] {
  const nonzero = spans.filter((s) => s.duration_ms > 0)

  // Filter to leaf spans: exclude any span that fully contains another
  const leaves = nonzero.filter((s) => {
    return !nonzero.some(
      (other) =>
        other !== s &&
        s.start_ms <= other.start_ms &&
        s.end_ms >= other.end_ms &&
        other.duration_ms > 0,
    )
  })

  // Sort chronologically and merge consecutive same-service spans
  leaves.sort((a, b) => a.start_ms - b.start_ms)
  const hops: ServiceHop[] = []
  for (const span of leaves) {
    const last = hops[hops.length - 1]
    if (last && last.service === span.service) {
      last.duration_ms = Math.round((last.duration_ms + span.duration_ms) * 10) / 10
      last.steps.push(span.name)
      if (span.status === 'error') last.status = 'error'
    } else {
      hops.push({
        service: span.service,
        duration_ms: span.duration_ms,
        status: span.status,
        steps: [span.name],
      })
    }
  }
  return hops
}

function BackButton() {
  const navigate = useNavigate()
  return (
    <Button
      variant="ghost"
      size="sm"
      className="-ml-3 mb-3"
      icon={<ArrowLeft size={16} aria-hidden />}
      onClick={() => navigate('/admin/traces')}
    >
      Back to traces
    </Button>
  )
}

export default function TraceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [showRawSpans, setShowRawSpans] = useState(false)
  const [selectedSpan, setSelectedSpan] = useState<TraceSpan | null>(null)

  const { data: trace, isLoading, error, refetch } = useQuery({
    queryKey: ['trace', id],
    queryFn: () => fetchTrace(id!),
    enabled: !!id,
  })

  const serviceHops = useMemo(
    () => (trace ? buildServiceHops(trace.spans) : []),
    [trace],
  )

  if (isLoading) {
    return (
      <div className="mx-auto max-w-6xl" role="status" aria-label="Loading the trace">
        <BackButton />
        <Skeleton className="mb-2 h-7 w-2/3" />
        <Skeleton className="mb-6 h-4 w-1/3" />
        <Card className="space-y-3">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-6" />
          ))}
        </Card>
      </div>
    )
  }

  if (error || !trace) {
    const missing = errorStatus(error) === 404 || !error
    return (
      <div className="mx-auto max-w-6xl">
        <BackButton />
        <Card>
          <ErrorState
            title={missing ? 'Trace not found' : "Couldn't load this trace"}
            message={missing ? 'It may have been cleaned up, or the link is wrong.' : errorMessage(error)}
            onRetry={missing ? undefined : () => refetch()}
          />
        </Card>
      </div>
    )
  }

  const total = trace.total_duration_ms || 1

  return (
    <div className="mx-auto max-w-6xl">
      <BackButton />
      <PageHeader
        className="items-start"
        title={<span className="break-words">{trace.user_command ?? 'Request trace'}</span>}
        description={
          <span className="inline-flex flex-wrap items-center gap-x-1.5 break-words">
            <SourceIcon source={trace.source} />
            {sourceLabel(trace.source)}
            {trace.node_id && ` (${trace.node_id})`}
            <span aria-hidden>·</span>
            {trace.request_type}
            <span aria-hidden>·</span>
            {formatServerTime(trace.created_at)}
          </span>
        }
        actions={
          <div className="flex items-center gap-3">
            <TraceStatus status={trace.status} />
            <span className="text-title text-text tabular-nums">{formatDuration(trace.total_duration_ms)}</span>
          </div>
        }
      />

      <div className="space-y-4">
        {trace.error_message && (
          <div role="alert" className="flex items-start gap-2 rounded-card bg-danger-soft p-3 text-small text-text">
            <AlertTriangle size={16} aria-hidden className="mt-0.5 shrink-0 text-danger" />
            <p className="min-w-0 break-words">{trace.error_message}</p>
          </div>
        )}

        <Card as="section" aria-labelledby="trace-flow">
          <CardHeader
            id="trace-flow"
            title="Service flow"
            description={serviceHops.map((h) => SERVICE_LABELS[h.service] ?? h.service).join(' → ')}
          />
          {serviceHops.length === 0 ? (
            <p className="text-small text-text-muted">No timed steps were recorded for this request.</p>
          ) : (
            <ol className="space-y-3">
              {serviceHops.map((hop, i) => {
                const pct = Math.min((hop.duration_ms / total) * 100, 100)
                const color = spanColor(hop.service, hop.status)
                const label = SERVICE_LABELS[hop.service] ?? hop.service
                return (
                  <li
                    key={`${hop.service}-${i}`}
                    className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 sm:grid-cols-[10rem_minmax(0,1fr)_4.5rem]"
                  >
                    <span className="flex min-w-0 items-center gap-2 text-small font-medium text-text">
                      <span aria-hidden className="size-2.5 shrink-0 rounded-pill" style={{ backgroundColor: color }} />
                      <span className="truncate">{label}</span>
                      {hop.status === 'error' && <span className="text-caption text-danger">error</span>}
                    </span>
                    <span className="text-right text-small text-text tabular-nums sm:col-start-3">
                      {formatDuration(hop.duration_ms)}
                    </span>
                    <span className="col-span-2 min-w-0 sm:col-span-1 sm:col-start-2 sm:row-start-1">
                      <span aria-hidden className="block h-2 overflow-hidden rounded-pill bg-surface-alt">
                        <span className="block h-full rounded-pill" style={{ width: `${Math.max(pct, 1)}%`, backgroundColor: color }} />
                      </span>
                      <span className="mt-1 block truncate text-caption text-text-muted" title={hop.steps.join(', ')}>
                        {hop.steps.join(', ')}
                      </span>
                    </span>
                  </li>
                )
              })}
            </ol>
          )}
          <p className="mt-4 font-mono text-caption break-all text-text-muted">
            <span className="font-sans">Conversation </span>
            {trace.conversation_id}
          </p>
        </Card>

        <Card as="section" aria-labelledby="trace-spans">
          <CardHeader
            id="trace-spans"
            className={showRawSpans ? undefined : 'mb-0'}
            title="Raw spans"
            description={`${trace.spans.length} ${trace.spans.length === 1 ? 'span' : 'spans'}, on the request's own timeline`}
            actions={
              <Button
                size="sm"
                aria-expanded={showRawSpans}
                onClick={() => {
                  setShowRawSpans((v) => !v)
                  setSelectedSpan(null)
                }}
              >
                {showRawSpans ? 'Hide spans' : 'Show spans'}
              </Button>
            }
          />

          {showRawSpans && (
            <ul className="space-y-0.5">
              {trace.spans.map((span, i) => {
                const pct = Math.min((span.duration_ms / total) * 100, 100)
                const leftPct = (span.start_ms / total) * 100
                const isSelected = selectedSpan === span
                return (
                  <li key={`${span.name}-${i}`}>
                    <button
                      type="button"
                      aria-pressed={isSelected}
                      onClick={() => setSelectedSpan(isSelected ? null : span)}
                      className={cn(
                        'grid min-h-10 w-full grid-cols-[6rem_minmax(0,1fr)_3.5rem] items-center gap-2 rounded-control px-2 text-left transition-colors duration-(--duration-hover) hover:bg-surface-alt sm:grid-cols-[9rem_minmax(0,1fr)_3.5rem_5rem]',
                        isSelected && 'bg-primary-soft hover:bg-primary-soft',
                      )}
                    >
                      <span className="truncate text-right text-caption text-text-muted" title={span.name}>
                        {span.name}
                      </span>
                      <span aria-hidden className="relative block h-3 overflow-hidden rounded-pill bg-surface-alt">
                        <span
                          className="absolute top-0 h-full rounded-pill"
                          style={{
                            left: `${leftPct}%`,
                            width: `${Math.max(pct, 0.5)}%`,
                            minWidth: '2px',
                            backgroundColor: spanColor(span.service, span.status),
                          }}
                        />
                      </span>
                      <span className="text-right text-caption text-text tabular-nums">{formatDuration(span.duration_ms)}</span>
                      <span className="hidden truncate text-caption text-text-muted sm:inline">{span.service}</span>
                    </button>
                  </li>
                )
              })}
            </ul>
          )}

          {selectedSpan && showRawSpans && (
            <div className="mt-3 border-t border-border pt-3">
              <dl className="grid max-w-md grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-small [&_dd]:break-words">
                <dt className="text-text-muted">Service</dt>
                <dd className="text-text">{selectedSpan.service}</dd>
                <dt className="text-text-muted">Duration</dt>
                <dd className="text-text tabular-nums">{formatDuration(selectedSpan.duration_ms)}</dd>
                <dt className="text-text-muted">Start</dt>
                <dd className="text-text tabular-nums">{formatDuration(selectedSpan.start_ms)}</dd>
                <dt className="text-text-muted">End</dt>
                <dd className="text-text tabular-nums">{formatDuration(selectedSpan.end_ms)}</dd>
                <dt className="text-text-muted">Status</dt>
                <dd className={selectedSpan.status === 'ok' ? 'text-success' : 'text-danger'}>{selectedSpan.status}</dd>
                {Object.entries(selectedSpan.metadata || {}).map(([key, value]) => (
                  <div key={key} className="contents">
                    <dt className="text-text-muted">{key}</dt>
                    <dd className="text-text tabular-nums">{String(value)}</dd>
                  </div>
                ))}
              </dl>
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
