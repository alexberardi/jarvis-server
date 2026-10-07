import { useEffect, useMemo, useState } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Pause, Play, RefreshCw, ScrollText } from 'lucide-react'
import { getLogs, getLogSources, LOG_LEVELS, tailLogs, type LogEntry, type LogFilters, type TailState } from '@/api/logs'
import { buttonClass, inputClass } from '@/components/models/styles'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

/** How many live entries the page keeps above the fetched pages. */
const TAIL_CAP = 1000

const SINCE_OPTIONS: { label: string; ms: number | null }[] = [
  { label: 'Any time', ms: null },
  { label: 'Last 15 minutes', ms: 15 * 60_000 },
  { label: 'Last hour', ms: 60 * 60_000 },
  { label: 'Last 24 hours', ms: 24 * 60 * 60_000 },
  { label: 'Last 7 days', ms: 7 * 24 * 60 * 60_000 },
]

const levelTone: Record<string, string> = {
  DEBUG: 'text-[var(--color-text-muted)]',
  INFO: 'text-blue-500',
  WARNING: 'text-amber-500',
  ERROR: 'text-red-500',
  CRITICAL: 'text-red-600 font-semibold',
}

const selectClass =
  'rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1.5 text-sm text-[var(--color-text)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]'

function formatTime(ts: string): string {
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString(undefined, { hour12: false })
}

function LogRow({ e }: { e: LogEntry }) {
  const [open, setOpen] = useState(false)
  const hasContext = e.context && Object.keys(e.context).length > 0
  return (
    <li className="border-b border-[var(--color-border)] px-2 py-1 font-mono text-xs last:border-0">
      <button
        type="button"
        className="flex w-full items-start gap-2 text-left"
        onClick={() => hasContext && setOpen(!open)}
        aria-expanded={hasContext ? open : undefined}
      >
        <span className="shrink-0 text-[var(--color-text-muted)]">{formatTime(e.timestamp)}</span>
        <span className={cn('w-16 shrink-0', levelTone[e.level] ?? '')}>{e.level}</span>
        <span className="w-28 shrink-0 truncate text-[var(--color-text-muted)]" title={e.node_id ? `node ${e.node_id}` : e.service}>
          {e.node_id ? `${e.service}@${e.node_id}` : e.service}
        </span>
        <span className="min-w-0 flex-1 whitespace-pre-wrap break-words text-[var(--color-text)]">{e.message}</span>
      </button>
      {open && hasContext && (
        <pre className="mt-1 overflow-x-auto rounded bg-[var(--color-surface-alt)] p-2 text-[11px] text-[var(--color-text-muted)]">
          {JSON.stringify(e.context, null, 2)}
        </pre>
      )}
    </li>
  )
}

const TAIL_TEXT: Record<TailState, string> = {
  connecting: 'connecting…',
  live: 'live',
  reconnecting: 'reconnecting…',
  stopped: 'stopped',
}

/**
 * LogsPage (AD9) reads jarvisd's own log store: filter by module or node, minimum level, time
 * and text, page back through older entries, and follow new ones live.
 */
export default function LogsPage() {
  const [service, setService] = useState('')
  const [node, setNode] = useState('')
  const [minLevel, setMinLevel] = useState('')
  const [sinceMs, setSinceMs] = useState<number | null>(null)
  const [since, setSince] = useState<string | undefined>(undefined)
  const [text, setText] = useState('')
  const [q, setQ] = useState('')
  const [live, setLive] = useState(false)
  const [tailState, setTailState] = useState<TailState>('stopped')
  // Live entries, tagged with the tail session (filters) they belong to.
  const [tail, setTail] = useState<{ session: string; entries: LogEntry[] }>({ session: '', entries: [] })

  // Debounce the free-text filter.
  useEffect(() => {
    const t = setTimeout(() => setQ(text), 300)
    return () => clearTimeout(t)
  }, [text])

  const filters: LogFilters = useMemo(
    () => ({
      service,
      node_id: node,
      min_level: minLevel,
      q,
      since,
    }),
    [service, node, minLevel, q, since],
  )
  const session = JSON.stringify(filters)

  const sources = useQuery({ queryKey: ['log-sources'], queryFn: () => getLogSources(), staleTime: 60_000 })
  const pages = useInfiniteQuery({
    queryKey: ['logs', filters],
    queryFn: ({ pageParam }) => getLogs(filters, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    staleTime: 5_000,
  })

  const newestFetched = pages.data?.pages[0]?.logs[0]?.id

  // The live tail: restarted whenever the filters change, resuming after the newest entry the
  // page already shows, so nothing between the fetch and the stream is lost.
  useEffect(() => {
    if (!live || pages.isLoading) return
    const ctrl = new AbortController()
    void tailLogs({
      filters,
      after: newestFetched,
      signal: ctrl.signal,
      onState: (s) => setTailState(s),
      onEntry: (e) =>
        setTail((t) => ({ session, entries: [e, ...(t.session === session ? t.entries : [])].slice(0, TAIL_CAP) })),
    })
    return () => ctrl.abort()
    // newestFetched is read once per (re)start on purpose: later pages must not restart it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, filters, session, pages.isLoading])

  const rows = useMemo(() => {
    const fetched = pages.data?.pages.flatMap((p) => p.logs) ?? []
    const seen = new Set(fetched.map((e) => e.id))
    const live = tail.session === session ? tail.entries : []
    return [...live.filter((e) => !seen.has(e.id)), ...fetched]
  }, [pages.data, tail, session])

  return (
    <div className="mx-auto max-w-6xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
            <ScrollText size={20} /> Logs
          </h1>
          <p className="text-sm text-[var(--color-text-muted)]">jarvisd's modules and the nodes that report to it.</p>
        </div>
        <div className="flex items-center gap-2">
          {live && (
            <span
              className={cn('text-xs', tailState === 'live' ? 'text-green-500' : 'text-amber-500')}
              data-testid="tail-state"
            >
              ● {TAIL_TEXT[tailState]}
            </span>
          )}
          <button type="button" className={live ? buttonClass.secondary : buttonClass.primary} onClick={() => setLive(!live)}>
            {live ? <Pause size={12} /> : <Play size={12} />} {live ? 'Stop live tail' : 'Live tail'}
          </button>
          <button
            type="button"
            className={buttonClass.icon}
            title="Refresh"
            aria-label="Refresh"
            onClick={() => {
              setTail({ session: '', entries: [] })
              void pages.refetch()
            }}
          >
            <RefreshCw size={14} className={cn(pages.isFetching && 'animate-spin')} />
          </button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="log-service">Module</label>
        <select id="log-service" className={selectClass} value={service} onChange={(e) => setService(e.target.value)}>
          <option value="">All modules</option>
          {(sources.data?.services ?? []).map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-node">Node</label>
        <select id="log-node" className={selectClass} value={node} onChange={(e) => setNode(e.target.value)}>
          <option value="">All nodes</option>
          {(sources.data?.nodes ?? []).map((n) => (
            <option key={n} value={n}>{n}</option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-level">Minimum level</label>
        <select id="log-level" className={selectClass} value={minLevel} onChange={(e) => setMinLevel(e.target.value)}>
          <option value="">Any level</option>
          {LOG_LEVELS.map((l) => (
            <option key={l} value={l}>{l} and worse</option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-since">Time</label>
        <select
          id="log-since"
          className={selectClass}
          value={sinceMs ?? ''}
          onChange={(e) => {
            const ms = e.target.value ? Number(e.target.value) : null
            setSinceMs(ms)
            setSince(ms ? new Date(Date.now() - ms).toISOString() : undefined)
          }}
        >
          {SINCE_OPTIONS.map((o) => (
            <option key={o.label} value={o.ms ?? ''}>{o.label}</option>
          ))}
        </select>
        <input
          aria-label="Search logs"
          className={cn(inputClass, 'w-56')}
          placeholder="Search text"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      </div>

      <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
        {pages.isLoading && <p className="p-4 text-sm text-[var(--color-text-muted)]">Loading…</p>}
        {pages.isError && <p className="p-4 text-sm text-red-500">{errorMessage(pages.error, 'Could not read the logs')}</p>}
        {!pages.isLoading && rows.length === 0 && !pages.isError && (
          <p className="p-4 text-sm text-[var(--color-text-muted)]">No log entries match.</p>
        )}
        {rows.length > 0 && (
          <ul aria-label="Log entries">
            {rows.map((e) => (
              <LogRow key={e.id} e={e} />
            ))}
          </ul>
        )}
      </div>

      {pages.hasNextPage && (
        <div className="flex justify-center">
          <button
            type="button"
            className={buttonClass.secondary}
            disabled={pages.isFetchingNextPage}
            onClick={() => void pages.fetchNextPage()}
          >
            {pages.isFetchingNextPage ? 'Loading…' : 'Load older'}
          </button>
        </div>
      )}
    </div>
  )
}
