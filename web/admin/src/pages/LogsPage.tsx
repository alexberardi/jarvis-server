import { useEffect, useMemo, useState } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, Pause, Play, RefreshCw, ScrollText, SearchX } from 'lucide-react'
import { getLogs, getLogSources, LOG_LEVELS, tailLogs, type LogEntry, type LogFilters, type TailState } from '@/api/logs'
import { Badge, Button, Card, EmptyState, ErrorState, Input, PageHeader, Select, SkeletonList, type BadgeTone } from '@/components/ui'
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

// Levels are coloured text, not chips: a chip on each of 200 rows would be the loudest thing on the page.
const levelTone: Record<string, string> = {
  DEBUG: 'text-text-muted',
  INFO: 'text-text-muted',
  WARNING: 'text-warning font-semibold',
  ERROR: 'text-danger font-semibold',
  CRITICAL: 'text-danger font-bold',
}

function formatTime(ts: string): string {
  const d = new Date(ts)
  return Number.isNaN(d.getTime())
    ? ts
    : d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}

function LogRow({ e }: { e: LogEntry }) {
  const [open, setOpen] = useState(false)
  const hasContext = e.context && Object.keys(e.context).length > 0
  const source = e.node_id ? `${e.service}@${e.node_id}` : e.service
  return (
    <li className="text-small">
      <button
        type="button"
        className={cn(
          'grid w-full grid-cols-[auto_auto_minmax(0,1fr)_auto] items-baseline gap-x-3 gap-y-0.5 px-4 py-1.5 text-left',
          'lg:grid-cols-[9.5rem_5.5rem_9rem_minmax(0,1fr)_1rem]',
          hasContext ? 'transition-colors duration-(--duration-hover) hover:bg-surface-alt' : 'cursor-default',
        )}
        onClick={() => hasContext && setOpen(!open)}
        aria-expanded={hasContext ? open : undefined}
      >
        <span className="text-caption whitespace-nowrap text-text-muted tabular-nums">{formatTime(e.timestamp)}</span>
        <span className={cn('text-caption', levelTone[e.level] ?? 'text-text-muted')}>{e.level}</span>
        <span className="min-w-0 truncate text-caption text-text-muted" title={e.node_id ? `node ${e.node_id}` : e.service}>
          {source}
        </span>
        <span className="col-span-4 row-start-2 min-w-0 font-mono text-small break-words whitespace-pre-wrap text-text lg:col-span-1 lg:col-start-4 lg:row-start-1">
          {e.message}
        </span>
        <span className="col-start-4 row-start-1 flex justify-end self-center text-text-muted lg:col-start-5" aria-hidden>
          {hasContext && (open ? <ChevronDown size={14} /> : <ChevronRight size={14} />)}
        </span>
      </button>
      {open && hasContext && (
        <pre className="mx-4 mb-2 overflow-x-auto rounded-control bg-surface-alt p-3 font-mono text-caption text-text">
          {JSON.stringify(e.context, null, 2)}
        </pre>
      )}
    </li>
  )
}

const TAIL_TONE: Record<TailState, BadgeTone> = {
  connecting: 'warning',
  live: 'success',
  reconnecting: 'warning',
  stopped: 'neutral',
}

const TAIL_TEXT: Record<TailState, string> = {
  connecting: 'Connecting…',
  live: 'Live',
  reconnecting: 'Reconnecting…',
  stopped: 'Stopped',
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

  const filtered = Boolean(service || node || minLevel || sinceMs || q)
  const clearFilters = () => {
    setService('')
    setNode('')
    setMinLevel('')
    setSinceMs(null)
    setSince(undefined)
    setText('')
    setQ('')
  }

  let body
  if (pages.isLoading) {
    body = <SkeletonList rows={8} label="Loading logs" />
  } else if (pages.isError) {
    body = <ErrorState title="Couldn't read the logs" message={errorMessage(pages.error)} onRetry={() => pages.refetch()} />
  } else if (rows.length === 0) {
    body = filtered ? (
      <EmptyState
        icon={<SearchX size={20} />}
        title="No log entries match"
        description="Try a lower level, a longer time or another module."
        action={<Button onClick={clearFilters}>Clear filters</Button>}
      />
    ) : (
      <EmptyState icon={<ScrollText size={20} />} title="Nothing logged yet" description="Entries appear here as the server and its nodes run." />
    )
  } else {
    body = (
      <ul aria-label="Log entries" className="divide-y divide-border">
        {rows.map((e) => (
          <LogRow key={e.id} e={e} />
        ))}
      </ul>
    )
  }

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Logs"
        description="What Jarvis Automation's modules, and the nodes that report to it, have logged."
        actions={
          <>
            {live && (
              <Badge tone={TAIL_TONE[tailState]} dot data-testid="tail-state">
                {TAIL_TEXT[tailState]}
              </Badge>
            )}
            <Button
              variant={live ? 'secondary' : 'primary'}
              icon={live ? <Pause size={16} aria-hidden /> : <Play size={16} aria-hidden />}
              onClick={() => setLive(!live)}
            >
              {live ? 'Stop live tail' : 'Live tail'}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              title="Refresh"
              aria-label="Refresh"
              onClick={() => {
                setTail({ session: '', entries: [] })
                void pages.refetch()
              }}
            >
              <RefreshCw size={16} aria-hidden className={cn(pages.isFetching && 'motion-safe:animate-spin')} />
            </Button>
          </>
        }
      />

      <div className="mb-4 grid grid-cols-2 gap-2 lg:grid-cols-[repeat(4,minmax(0,1fr))_minmax(0,1.5fr)]">
        <Select
          id="log-service"
          aria-label="Module"
          value={service}
          onValueChange={setService}
          options={[{ value: '', label: 'All modules' }, ...(sources.data?.services ?? []).map((s) => ({ value: s, label: s }))]}
        />
        <Select
          id="log-node"
          aria-label="Node"
          value={node}
          onValueChange={setNode}
          options={[{ value: '', label: 'All nodes' }, ...(sources.data?.nodes ?? []).map((n) => ({ value: n, label: n }))]}
        />
        <Select
          id="log-level"
          aria-label="Minimum level"
          value={minLevel}
          onValueChange={setMinLevel}
          options={[{ value: '', label: 'Any level' }, ...LOG_LEVELS.map((l) => ({ value: l, label: `${l} and worse` }))]}
        />
        <Select
          id="log-since"
          aria-label="Time"
          value={sinceMs == null ? '' : String(sinceMs)}
          onValueChange={(v) => {
            const ms = v ? Number(v) : null
            setSinceMs(ms)
            setSince(ms ? new Date(Date.now() - ms).toISOString() : undefined)
          }}
          options={SINCE_OPTIONS.map((o) => ({ value: o.ms == null ? '' : String(o.ms), label: o.label }))}
        />
        <Input
          type="search"
          aria-label="Search logs"
          wrapperClassName="col-span-2 lg:col-span-1"
          placeholder="Search messages"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      </div>

      <Card flush>{body}</Card>

      {pages.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button loading={pages.isFetchingNextPage} onClick={() => void pages.fetchNextPage()}>
            Load older entries
          </Button>
        </div>
      )}
    </div>
  )
}
