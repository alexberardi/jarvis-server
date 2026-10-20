import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { Activity, ChevronDown, ChevronRight, SearchX } from 'lucide-react'
import { fetchTraces, type TraceListItem } from '@/api/traces'
import { fetchAllHouseholds, fetchAllNodes, type AdminNode } from '@/api/admin'
import { Button, Card, EmptyState, ErrorState, PageHeader, Select, SkeletonList, Table, TBody, TD, TH, THead, TR } from '@/components/ui'
import { SourceIcon, TraceStatus } from '@/components/traces/TraceBits'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { formatDuration, sourceLabel } from '@/lib/traces'
import { cn } from '@/lib/utils'

function formatTime(iso: string): string {
  return formatServerTime(iso, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

export interface ConversationGroup {
  conversation_id: string
  traces: TraceListItem[]
  newest_at: string
  user_command: string | null
  assistant_message: string | null
  source: string
  node_id: string | null
  household_id: string | null
  total_duration_ms: number
  status: string
}

function groupByConversation(traces: TraceListItem[]): ConversationGroup[] {
  const groups = new Map<string, ConversationGroup>()
  for (const t of traces) {
    const g = groups.get(t.conversation_id)
    if (!g) {
      groups.set(t.conversation_id, {
        conversation_id: t.conversation_id,
        traces: [t],
        newest_at: t.created_at,
        user_command: t.user_command,
        assistant_message: t.assistant_message,
        source: t.source,
        node_id: t.node_id,
        household_id: t.household_id,
        total_duration_ms: t.total_duration_ms,
        status: t.status,
      })
      continue
    }
    g.traces.push(t)
    g.total_duration_ms += t.total_duration_ms
    if (t.created_at > g.newest_at) g.newest_at = t.created_at
    // Prefer the user command + assistant message from a real exchange over warmup
    if (!g.user_command && t.user_command) g.user_command = t.user_command
    if (!g.assistant_message && t.assistant_message) g.assistant_message = t.assistant_message
    if (t.status !== 'ok') g.status = t.status
  }
  return Array.from(groups.values()).sort((a, b) =>
    a.newest_at < b.newest_at ? 1 : -1,
  )
}

function byOldest(traces: TraceListItem[]): TraceListItem[] {
  return traces.slice().sort((a, b) => (a.created_at < b.created_at ? -1 : 1))
}

const noTraces = (
  <EmptyState
    icon={<Activity size={20} />}
    title="No traces yet"
    description="A trace appears each time someone talks to Jarvis, on a node or in the app: what they said, the answer, and how long each step took."
  />
)

/** TraceCards is the phone layout of the traces table: a row per conversation, opening onto its traces. */
export function TraceCards({
  groups,
  expanded,
  onToggle,
  onOpen,
  nodeLabel,
  householdName,
  empty = noTraces,
}: {
  groups: ConversationGroup[]
  expanded: Set<string>
  onToggle: (conversationId: string) => void
  onOpen: (traceId: string) => void
  nodeLabel: (nodeId: string | null) => string
  householdName: (householdId: string) => string
  /** What shows when there are no conversations. */
  empty?: ReactNode
}) {
  if (groups.length === 0) return <>{empty}</>
  return (
    <ul className="divide-y divide-border lg:hidden" aria-label="Conversations" data-testid="trace-cards">
      {groups.map((g) => {
        const isExpanded = expanded.has(g.conversation_id)
        const household = g.household_id ? householdName(g.household_id) : ''
        return (
          <li key={g.conversation_id}>
            <button
              type="button"
              className="block w-full space-y-1 px-4 py-3 text-left transition-colors duration-(--duration-hover) hover:bg-surface-alt"
              aria-expanded={isExpanded}
              onClick={() => onToggle(g.conversation_id)}
            >
              <span className="flex items-center justify-between gap-2 text-caption text-text-muted">
                <span className="flex min-w-0 items-center gap-1.5">
                  <SourceIcon source={g.source} />
                  <span className="min-w-0 truncate">
                    {formatTime(g.newest_at)} · {nodeLabel(g.node_id)}
                    {household && ` · ${household}`}
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-2 tabular-nums">
                  {formatDuration(g.total_duration_ms)}
                  <TraceStatus status={g.status} />
                  <span className="flex items-center gap-0.5">
                    {g.traces.length}
                    {isExpanded ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
                  </span>
                </span>
              </span>
              <span className="line-clamp-2 block text-body break-words text-text">{g.user_command ?? '-'}</span>
              {g.assistant_message && (
                <span className="line-clamp-2 block text-small break-words text-text-muted">{g.assistant_message}</span>
              )}
            </button>
            {isExpanded && (
              <ul className="divide-y divide-border border-t border-border bg-surface-alt">
                {byOldest(g.traces).map((t) => (
                  <li key={t.id}>
                    <button
                      type="button"
                      className="flex min-h-11 w-full items-center justify-between gap-2 py-2 pr-4 pl-10 text-left text-small transition-colors duration-(--duration-hover) hover:bg-border"
                      onClick={() => onOpen(t.id)}
                    >
                      <span className="min-w-0 truncate text-text">
                        {formatTime(t.created_at)} · {t.request_type}
                      </span>
                      <span className="flex shrink-0 items-center gap-2 text-caption text-text-muted tabular-nums">
                        {formatDuration(t.total_duration_ms)}
                        <TraceStatus status={t.status} />
                        {t.span_count} spans
                        <ChevronRight size={14} aria-hidden />
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </li>
        )
      })}
    </ul>
  )
}

/** TraceTable is the traces table from lg: a row per conversation, its traces under it when open. */
function TraceTable({
  groups,
  expanded,
  onToggle,
  onOpen,
  nodeLabel,
  householdName,
}: {
  groups: ConversationGroup[]
  expanded: Set<string>
  onToggle: (conversationId: string) => void
  onOpen: (traceId: string) => void
  nodeLabel: (nodeId: string | null) => string
  householdName: (householdId: string) => string
}) {
  const row = 'cursor-pointer transition-colors duration-(--duration-hover) hover:bg-surface-alt'
  return (
    <div className="hidden lg:block">
      <Table caption="Conversations">
        <THead>
          <TR>
            <TH className="w-10 px-2">
              <span className="sr-only">Open</span>
            </TH>
            <TH>Time</TH>
            <TH>Source</TH>
            <TH>Node</TH>
            <TH>User said</TH>
            <TH>Jarvis said</TH>
            <TH className="text-right">Duration</TH>
            <TH>Status</TH>
            <TH className="text-right">Traces</TH>
          </TR>
        </THead>
        <TBody>
          {groups.map((g) => {
            const isExpanded = expanded.has(g.conversation_id)
            const household = g.household_id ? householdName(g.household_id) : ''
            return (
              <Fragment key={g.conversation_id}>
                <TR className={row} onClick={() => onToggle(g.conversation_id)}>
                  <TD className="px-2">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8"
                      aria-expanded={isExpanded}
                      aria-label={`${isExpanded ? 'Hide' : 'Show'} this conversation's traces`}
                      onClick={(e) => {
                        e.stopPropagation()
                        onToggle(g.conversation_id)
                      }}
                    >
                      {isExpanded ? <ChevronDown size={16} aria-hidden /> : <ChevronRight size={16} aria-hidden />}
                    </Button>
                  </TD>
                  <TD className="whitespace-nowrap tabular-nums">{formatTime(g.newest_at)}</TD>
                  <TD className="text-text-muted">
                    <span className="inline-flex items-center gap-1.5">
                      <SourceIcon source={g.source} />
                      {sourceLabel(g.source)}
                    </span>
                  </TD>
                  <TD className="whitespace-nowrap text-text-muted">
                    {nodeLabel(g.node_id)}
                    {household && <span className="text-small"> · {household}</span>}
                  </TD>
                  <TD className="max-w-xs truncate">{g.user_command ?? '-'}</TD>
                  <TD className="max-w-xs truncate text-text-muted">{g.assistant_message ?? '-'}</TD>
                  <TD className="text-right tabular-nums">{formatDuration(g.total_duration_ms)}</TD>
                  <TD>
                    <TraceStatus status={g.status} />
                  </TD>
                  <TD className="text-right text-text-muted tabular-nums">{g.traces.length}</TD>
                </TR>
                {isExpanded &&
                  byOldest(g.traces).map((trace) => (
                    <TR key={trace.id} className={cn(row, 'bg-surface-alt/60')} onClick={() => onOpen(trace.id)}>
                      <TD className="px-2" />
                      <TD className="whitespace-nowrap text-text-muted tabular-nums">
                        <Link
                          to={`/admin/traces/${trace.id}`}
                          className="rounded-control underline-offset-2 hover:underline"
                          onClick={(e) => e.stopPropagation()}
                        >
                          {formatTime(trace.created_at)}
                        </Link>
                      </TD>
                      <TD className="text-small text-text-muted">{trace.request_type}</TD>
                      <TD className="text-small whitespace-nowrap text-text-muted">{nodeLabel(trace.node_id)}</TD>
                      <TD className="max-w-xs truncate">{trace.user_command ?? '-'}</TD>
                      <TD className="max-w-xs truncate text-text-muted">{trace.assistant_message ?? '-'}</TD>
                      <TD className="text-right tabular-nums">{formatDuration(trace.total_duration_ms)}</TD>
                      <TD>
                        <TraceStatus status={trace.status} />
                      </TD>
                      <TD className="text-right text-text-muted tabular-nums">{trace.span_count}</TD>
                    </TR>
                  ))}
              </Fragment>
            )
          })}
        </TBody>
      </Table>
    </div>
  )
}

export default function TracesPage() {
  const navigate = useNavigate()
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [sourceFilter, setSourceFilter] = useState<string>('')
  const [householdFilter, setHouseholdFilter] = useState<string>('')
  const [nodeFilter, setNodeFilter] = useState<string>('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const householdsQuery = useQuery({
    queryKey: ['admin', 'households'],
    queryFn: fetchAllHouseholds,
    staleTime: 60_000,
  })

  const nodesQuery = useQuery({
    queryKey: ['admin', 'nodes'],
    queryFn: fetchAllNodes,
    staleTime: 60_000,
  })

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['traces', statusFilter, sourceFilter, householdFilter, nodeFilter],
    queryFn: () =>
      fetchTraces({
        limit: 100,
        ...(statusFilter ? { status: statusFilter } : {}),
        ...(sourceFilter ? { source: sourceFilter } : {}),
        ...(householdFilter ? { household_id: householdFilter } : {}),
        ...(nodeFilter ? { node_id: nodeFilter } : {}),
      }),
    refetchInterval: 10_000,
  })

  const nodesForDropdown: AdminNode[] = useMemo(() => {
    const all = nodesQuery.data ?? []
    return householdFilter
      ? all.filter((n) => n.household_id === householdFilter)
      : all
  }, [nodesQuery.data, householdFilter])

  const nodeNameById = useMemo(() => {
    const m = new Map<string, string>()
    for (const n of nodesQuery.data ?? []) m.set(n.node_id, n.name)
    return m
  }, [nodesQuery.data])

  const householdNameById = useMemo(() => {
    const m = new Map<string, string>()
    for (const h of householdsQuery.data ?? []) m.set(h.id, h.name)
    return m
  }, [householdsQuery.data])

  const groups = useMemo(
    () => (data ? groupByConversation(data.traces) : []),
    [data],
  )

  function toggleExpanded(conversationId: string): void {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(conversationId)) next.delete(conversationId)
      else next.add(conversationId)
      return next
    })
  }

  function onHouseholdChange(value: string): void {
    setHouseholdFilter(value)
    // If the currently-selected node is no longer in the chosen household, clear it
    if (value && nodeFilter) {
      const selected = (nodesQuery.data ?? []).find((n) => n.node_id === nodeFilter)
      if (selected && selected.household_id !== value) setNodeFilter('')
    }
  }

  const filtered = Boolean(statusFilter || sourceFilter || householdFilter || nodeFilter)
  function clearFilters(): void {
    setStatusFilter('')
    setSourceFilter('')
    setHouseholdFilter('')
    setNodeFilter('')
  }

  const shared = {
    groups,
    expanded,
    onToggle: toggleExpanded,
    onOpen: (id: string) => navigate(`/admin/traces/${id}`),
    nodeLabel: (id: string | null) => (id ? nodeNameById.get(id) ?? id.slice(0, 8) : '-'),
    householdName: (id: string) => householdNameById.get(id) ?? '',
  }

  let body
  if (isLoading) {
    body = <SkeletonList rows={6} label="Loading traces" />
  } else if (error) {
    body = <ErrorState title="Couldn't load traces" message={errorMessage(error)} onRetry={() => refetch()} />
  } else {
    body = (
      <>
        {/* Below lg (a phone, or a tablet next to the sidebar) the table becomes a row per conversation. */}
        <TraceCards
          {...shared}
          empty={
            filtered ? (
              <EmptyState
                icon={<SearchX size={20} />}
                title="No traces match these filters"
                action={<Button onClick={clearFilters}>Clear filters</Button>}
              />
            ) : undefined
          }
        />
        {groups.length > 0 && <TraceTable {...shared} />}
      </>
    )
  }

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Traces"
        description="Each request Jarvis handled: what was said, the answer, and how long each service took."
      />

      <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:w-auto">
          <Select
            aria-label="Household"
            value={householdFilter}
            onValueChange={onHouseholdChange}
            disabled={householdsQuery.isLoading}
            options={[{ value: '', label: 'All households' }, ...(householdsQuery.data ?? []).map((h) => ({ value: h.id, label: h.name }))]}
          />
          <Select
            aria-label="Node"
            value={nodeFilter}
            onValueChange={setNodeFilter}
            disabled={nodesQuery.isLoading}
            options={[{ value: '', label: 'All nodes' }, ...nodesForDropdown.map((n) => ({ value: n.node_id, label: n.name }))]}
          />
          <Select
            aria-label="Status"
            value={statusFilter}
            onValueChange={setStatusFilter}
            options={[
              { value: '', label: 'All statuses' },
              { value: 'ok', label: 'OK' },
              { value: 'error', label: 'Error' },
            ]}
          />
          <Select
            aria-label="Source"
            value={sourceFilter}
            onValueChange={setSourceFilter}
            options={[
              { value: '', label: 'All sources' },
              { value: 'node', label: 'Node' },
              { value: 'mobile', label: 'Mobile' },
            ]}
          />
        </div>
        {data && data.total > 0 && (
          <p className="text-small text-text-muted tabular-nums">
            {data.total} {data.total === 1 ? 'trace' : 'traces'} in {groups.length}{' '}
            {groups.length === 1 ? 'conversation' : 'conversations'}
          </p>
        )}
      </div>

      <Card flush className="overflow-hidden">
        {body}
      </Card>
    </div>
  )
}
