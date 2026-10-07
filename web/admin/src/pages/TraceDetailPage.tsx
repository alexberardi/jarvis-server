import { useMemo, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { fetchTrace, type TraceSpan } from '@/api/traces'

function formatDuration(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

const SERVICE_COLORS: Record<string, string> = {
  cc: '#9e9e9e',
  llm_proxy: '#42a5f5',
  node: '#66bb6a',
  tts: '#ffa726',
  whisper: '#ab47bc',
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

export default function TraceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [showRawSpans, setShowRawSpans] = useState(false)
  const [selectedSpan, setSelectedSpan] = useState<TraceSpan | null>(null)

  const { data: trace, isLoading, error } = useQuery({
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
      <div className="flex justify-center py-12">
        <div className="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-primary)] border-t-transparent" />
      </div>
    )
  }

  if (error || !trace) {
    return (
      <div className="space-y-4">
        <button
          className="text-sm text-[var(--color-primary)] hover:underline"
          onClick={() => navigate('/traces')}
        >
          Back to traces
        </button>
        <div className="rounded-lg border border-red-300 bg-red-50 p-4 text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-400">
          Trace not found
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <button
        className="text-sm text-[var(--color-primary)] hover:underline"
        onClick={() => navigate('/traces')}
      >
        Back to traces
      </button>

      {/* Header */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-lg font-bold text-[var(--color-text)]">
              {trace.user_command ?? 'Request Trace'}
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-muted)]">
              {trace.source === 'mobile' ? '📱 Mobile' : '🔊 Node'}
              {trace.node_id && ` (${trace.node_id})`}
              {' · '}
              {trace.request_type}
              {' · '}
              {trace.created_at ? new Date(trace.created_at).toLocaleString() : ''}
            </p>
          </div>
          <div className="text-right">
            <div className="text-2xl font-bold tabular-nums text-[var(--color-text)]">
              {formatDuration(trace.total_duration_ms)}
            </div>
            <div className="flex items-center justify-end gap-1 mt-1">
              <span
                className={`inline-block h-2 w-2 rounded-full ${
                  trace.status === 'ok' ? 'bg-green-500' : 'bg-red-500'
                }`}
              />
              <span className="text-sm text-[var(--color-text-muted)]">{trace.status}</span>
            </div>
          </div>
        </div>
        {trace.error_message && (
          <div className="mt-3 rounded border border-red-300 bg-red-50 p-2 text-sm text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-400">
            {trace.error_message}
          </div>
        )}
        <p className="mt-2 text-xs text-[var(--color-text-muted)] font-mono">
          {trace.conversation_id}
        </p>
      </div>

      {/* Service flow */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <h2 className="mb-3 text-sm font-medium text-[var(--color-text-muted)]">
          Service Flow
        </h2>

        {/* Flow arrow */}
        <p className="mb-4 text-xs text-[var(--color-text-muted)] opacity-60">
          {serviceHops.map((h) => SERVICE_LABELS[h.service] ?? h.service).join(' → ')}
        </p>

        <div className="space-y-2">
          {serviceHops.map((hop, i) => {
            const pct = Math.min((hop.duration_ms / trace.total_duration_ms) * 100, 100)
            const color = hop.status === 'error' ? '#ef4444' : (SERVICE_COLORS[hop.service] ?? '#9e9e9e')
            const label = SERVICE_LABELS[hop.service] ?? hop.service

            return (
              <div key={`${hop.service}-${i}`} className="flex items-center gap-3">
                <span
                  className="inline-block h-2.5 w-2.5 rounded-full shrink-0"
                  style={{ backgroundColor: color }}
                />
                <span className="w-32 text-sm text-[var(--color-text)] shrink-0">
                  {label}
                </span>
                <div className="flex-1 h-6 rounded bg-[var(--color-surface-alt)] relative">
                  <div
                    className="absolute top-0 left-0 h-full rounded"
                    style={{
                      width: `${Math.max(pct, 1)}%`,
                      backgroundColor: color,
                      opacity: 0.7,
                    }}
                  />
                  <span className="absolute inset-0 flex items-center px-2 text-xs text-[var(--color-text)] opacity-70">
                    {hop.steps.join(', ')}
                  </span>
                </div>
                <span className="w-16 text-sm text-[var(--color-text)] text-right tabular-nums shrink-0">
                  {formatDuration(hop.duration_ms)}
                </span>
              </div>
            )
          })}
        </div>
      </div>

      {/* Raw spans (collapsible) */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <button
          className="text-sm text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
          onClick={() => { setShowRawSpans((v) => !v); setSelectedSpan(null) }}
        >
          {showRawSpans ? 'Hide' : 'Show'} raw spans ({trace.spans.length})
        </button>

        {showRawSpans && (
          <div className="mt-3 space-y-1">
            {trace.spans.map((span, i) => {
              const pct = Math.min((span.duration_ms / trace.total_duration_ms) * 100, 100)
              const leftPct = (span.start_ms / trace.total_duration_ms) * 100
              const color = span.status === 'error' ? '#ef4444' : (SERVICE_COLORS[span.service] ?? '#9e9e9e')
              const isSelected = selectedSpan === span

              return (
                <div
                  key={`${span.name}-${i}`}
                  className={`flex items-center gap-2 cursor-pointer rounded px-1 py-0.5 ${isSelected ? 'bg-[var(--color-surface-alt)]' : ''}`}
                  onClick={() => setSelectedSpan(isSelected ? null : span)}
                >
                  <span className="w-36 truncate text-xs text-[var(--color-text-muted)] text-right shrink-0">
                    {span.name}
                  </span>
                  <div className="flex-1 relative h-4 rounded bg-[var(--color-surface-alt)]">
                    <div
                      className="absolute top-0 h-full rounded"
                      style={{
                        left: `${leftPct}%`,
                        width: `${Math.max(pct, 0.5)}%`,
                        backgroundColor: color,
                        minWidth: '2px',
                      }}
                    />
                  </div>
                  <span className="w-14 text-xs text-[var(--color-text-muted)] text-right shrink-0 tabular-nums">
                    {formatDuration(span.duration_ms)}
                  </span>
                  <span className="w-16 text-xs text-[var(--color-text-muted)] opacity-60 shrink-0">
                    {span.service}
                  </span>
                </div>
              )
            })}
          </div>
        )}

        {/* Span detail */}
        {selectedSpan && showRawSpans && (
          <div className="mt-3 border-t border-[var(--color-border)] pt-3">
            <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm max-w-md">
              <dt className="text-[var(--color-text-muted)]">Service</dt>
              <dd className="text-[var(--color-text)]">{selectedSpan.service}</dd>
              <dt className="text-[var(--color-text-muted)]">Duration</dt>
              <dd className="text-[var(--color-text)] tabular-nums">{formatDuration(selectedSpan.duration_ms)}</dd>
              <dt className="text-[var(--color-text-muted)]">Start</dt>
              <dd className="text-[var(--color-text)] tabular-nums">{formatDuration(selectedSpan.start_ms)}</dd>
              <dt className="text-[var(--color-text-muted)]">End</dt>
              <dd className="text-[var(--color-text)] tabular-nums">{formatDuration(selectedSpan.end_ms)}</dd>
              <dt className="text-[var(--color-text-muted)]">Status</dt>
              <dd className={selectedSpan.status === 'ok' ? 'text-green-600' : 'text-red-600'}>
                {selectedSpan.status}
              </dd>
              {Object.entries(selectedSpan.metadata || {}).map(([key, value]) => (
                <div key={key} className="contents">
                  <dt className="text-[var(--color-text-muted)]">{key}</dt>
                  <dd className="text-[var(--color-text)] tabular-nums">{String(value)}</dd>
                </div>
              ))}
            </dl>
          </div>
        )}
      </div>
    </div>
  )
}
