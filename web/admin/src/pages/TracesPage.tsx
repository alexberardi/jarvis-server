import { Fragment, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { fetchTraces, type TraceListItem } from '@/api/traces'
import { fetchAllHouseholds, fetchAllNodes, type AdminNode } from '@/api/admin'

function formatDuration(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

interface ConversationGroup {
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

function sourceBadge(source: string): string {
  return source === 'mobile' ? '📱' : '🔊'
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

  const { data, isLoading, error } = useQuery({
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

  const selectClass =
    'rounded border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-sm text-[var(--color-text)]'

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-bold text-[var(--color-text)]">Request Traces</h1>
        <div className="flex flex-wrap items-center gap-2">
          <select
            className={selectClass}
            value={householdFilter}
            onChange={(e) => onHouseholdChange(e.target.value)}
            disabled={householdsQuery.isLoading}
          >
            <option value="">All households</option>
            {(householdsQuery.data ?? []).map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </select>
          <select
            className={selectClass}
            value={nodeFilter}
            onChange={(e) => setNodeFilter(e.target.value)}
            disabled={nodesQuery.isLoading}
          >
            <option value="">All nodes</option>
            {nodesForDropdown.map((n) => (
              <option key={n.node_id} value={n.node_id}>
                {n.name}
              </option>
            ))}
          </select>
          <select
            className={selectClass}
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
          >
            <option value="">All statuses</option>
            <option value="ok">OK</option>
            <option value="error">Error</option>
          </select>
          <select
            className={selectClass}
            value={sourceFilter}
            onChange={(e) => setSourceFilter(e.target.value)}
          >
            <option value="">All sources</option>
            <option value="node">Node</option>
            <option value="mobile">Mobile</option>
          </select>
        </div>
      </div>

      {isLoading && (
        <div className="flex justify-center py-12">
          <div className="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-primary)] border-t-transparent" />
        </div>
      )}

      {error && (
        <div className="rounded-lg border border-red-300 bg-red-50 p-4 text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-400">
          Failed to load traces
        </div>
      )}

      {data && (
        <>
          <p className="text-sm text-[var(--color-text-muted)]">
            {data.total} traces in {groups.length} conversation{groups.length === 1 ? '' : 's'}
          </p>
          <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--color-border)] bg-[var(--color-surface-alt)]">
                  <th className="w-8 px-2 py-2"></th>
                  <th className="px-3 py-2 text-left font-medium text-[var(--color-text-muted)]">Time</th>
                  <th className="px-3 py-2 text-left font-medium text-[var(--color-text-muted)]">Source</th>
                  <th className="px-3 py-2 text-left font-medium text-[var(--color-text-muted)]">Node</th>
                  <th className="px-3 py-2 text-left font-medium text-[var(--color-text-muted)]">User said</th>
                  <th className="px-3 py-2 text-left font-medium text-[var(--color-text-muted)]">Jarvis said</th>
                  <th className="px-3 py-2 text-right font-medium text-[var(--color-text-muted)]">Duration</th>
                  <th className="px-3 py-2 text-center font-medium text-[var(--color-text-muted)]">Status</th>
                  <th className="px-3 py-2 text-right font-medium text-[var(--color-text-muted)]">Traces</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => {
                  const isExpanded = expanded.has(g.conversation_id)
                  const traceCount = g.traces.length
                  const nodeLabel = g.node_id ? nodeNameById.get(g.node_id) ?? g.node_id.slice(0, 8) : '-'
                  return (
                    <Fragment key={g.conversation_id}>
                      <tr
                        className="border-b border-[var(--color-border)] cursor-pointer transition-colors hover:bg-[var(--color-surface-alt)]"
                        onClick={() => toggleExpanded(g.conversation_id)}
                      >
                        <td className="px-2 py-2 text-center text-[var(--color-text-muted)]">
                          {traceCount > 1 ? (isExpanded ? '▼' : '▶') : ''}
                        </td>
                        <td className="px-3 py-2 text-[var(--color-text)] whitespace-nowrap">
                          {formatTime(g.newest_at)}
                        </td>
                        <td className="px-3 py-2 text-[var(--color-text-muted)]">
                          <span className="inline-flex items-center gap-1">
                            {sourceBadge(g.source)}
                            {g.source}
                          </span>
                        </td>
                        <td className="px-3 py-2 text-[var(--color-text-muted)] whitespace-nowrap">
                          {nodeLabel}
                          {g.household_id && (
                            <span className="ml-1 text-xs opacity-60">
                              · {householdNameById.get(g.household_id) ?? ''}
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-[var(--color-text)] max-w-xs truncate">
                          {g.user_command ?? '-'}
                        </td>
                        <td className="px-3 py-2 text-[var(--color-text-muted)] max-w-xs truncate">
                          {g.assistant_message ?? '-'}
                        </td>
                        <td className="px-3 py-2 text-right text-[var(--color-text)] tabular-nums">
                          {formatDuration(g.total_duration_ms)}
                        </td>
                        <td className="px-3 py-2 text-center">
                          <span
                            className={`inline-block h-2 w-2 rounded-full ${
                              g.status === 'ok' ? 'bg-green-500' : 'bg-red-500'
                            }`}
                          />
                        </td>
                        <td className="px-3 py-2 text-right text-[var(--color-text-muted)] tabular-nums">
                          {traceCount}
                        </td>
                      </tr>
                      {isExpanded &&
                        g.traces
                          .slice()
                          .sort((a, b) => (a.created_at < b.created_at ? -1 : 1))
                          .map((trace) => (
                            <tr
                              key={trace.id}
                              className="border-b border-[var(--color-border)] cursor-pointer bg-[var(--color-surface-alt)]/40 transition-colors hover:bg-[var(--color-surface-alt)]"
                              onClick={() => navigate(`/traces/${trace.id}`)}
                            >
                              <td className="px-2 py-2"></td>
                              <td className="px-3 py-2 text-[var(--color-text-muted)] whitespace-nowrap">
                                <span className="inline-block w-3" />
                                {formatTime(trace.created_at)}
                              </td>
                              <td className="px-3 py-2 text-[var(--color-text-muted)] text-xs">
                                {trace.request_type}
                              </td>
                              <td className="px-3 py-2 text-[var(--color-text-muted)] whitespace-nowrap text-xs">
                                {trace.node_id ? nodeNameById.get(trace.node_id) ?? trace.node_id.slice(0, 8) : '-'}
                              </td>
                              <td className="px-3 py-2 text-[var(--color-text)] max-w-xs truncate">
                                {trace.user_command ?? '-'}
                              </td>
                              <td className="px-3 py-2 text-[var(--color-text-muted)] max-w-xs truncate">
                                {trace.assistant_message ?? '-'}
                              </td>
                              <td className="px-3 py-2 text-right text-[var(--color-text)] tabular-nums">
                                {formatDuration(trace.total_duration_ms)}
                              </td>
                              <td className="px-3 py-2 text-center">
                                <span
                                  className={`inline-block h-2 w-2 rounded-full ${
                                    trace.status === 'ok' ? 'bg-green-500' : 'bg-red-500'
                                  }`}
                                />
                              </td>
                              <td className="px-3 py-2 text-right text-[var(--color-text-muted)] tabular-nums">
                                {trace.span_count}
                              </td>
                            </tr>
                          ))}
                    </Fragment>
                  )
                })}
                {groups.length === 0 && (
                  <tr>
                    <td colSpan={9} className="px-3 py-8 text-center text-[var(--color-text-muted)]">
                      No traces yet
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  )
}
