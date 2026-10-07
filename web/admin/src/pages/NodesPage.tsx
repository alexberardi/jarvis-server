import { useMemo } from 'react'
import { RefreshCw, Cpu, Zap, ZapOff, Wifi, WifiOff } from 'lucide-react'
import { useNodeLiveness, useNodesView } from '@/hooks/useNodes'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'
import type { AdminNode } from '@/api/admin'
import type { NodeLiveness } from '@/api/nodes'

function formatSeen(iso: string | null): string {
  if (!iso) return 'never'
  // cc reports naive UTC timestamps (legacy shape).
  const t = new Date(/[zZ]|[+-]\d\d:?\d\d$/.test(iso) ? iso : `${iso}Z`)
  if (Number.isNaN(t.getTime())) return iso
  const secs = Math.max(0, Math.round((Date.now() - t.getTime()) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.round(secs / 60)}m ago`
  if (secs < 86400) return `${Math.round(secs / 3600)}h ago`
  return t.toLocaleString()
}

function NodeCard({ node, live }: { node: AdminNode; live?: NodeLiveness }) {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Cpu size={16} className="shrink-0 text-[var(--color-text-muted)]" />
          <span className="truncate font-medium text-[var(--color-text)]">{node.name}</span>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {live && (
            <span
              className={cn(
                'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
                live.online ? 'bg-green-500/10 text-green-500' : 'bg-amber-500/10 text-amber-500',
              )}
            >
              {live.online ? <Wifi size={10} /> : <WifiOff size={10} />}
              {live.online ? 'Online' : 'Offline'}
            </span>
          )}
          <span
            className={cn(
              'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
              node.is_active ? 'bg-green-500/10 text-green-500' : 'bg-red-500/10 text-red-500',
            )}
          >
            {node.is_active ? <Zap size={10} /> : <ZapOff size={10} />}
            {node.is_active ? 'Active' : 'Inactive'}
          </span>
        </div>
      </div>

      <div className="mt-3 space-y-1 text-xs text-[var(--color-text-muted)]">
        <p>
          <span className="font-medium">ID:</span> <code>{node.node_id}</code>
        </p>
        {live?.room && (
          <p>
            <span className="font-medium">Room:</span> {live.room}
          </p>
        )}
        {live && (
          <p>
            <span className="font-medium">Last seen:</span> {formatSeen(live.last_seen)}
            {live.last_seen_version && <> &middot; v{live.last_seen_version}</>}
          </p>
        )}
        {node.services.length > 0 && (
          <p>
            <span className="font-medium">Services:</span> {node.services.join(', ')}
          </p>
        )}
      </div>
    </div>
  )
}

function NodeGrid({ nodes, liveness }: { nodes: AdminNode[]; liveness: Map<string, NodeLiveness> }) {
  if (nodes.length === 0) {
    return (
      <p className="py-4 text-center text-sm text-[var(--color-text-muted)]">No nodes in this household</p>
    )
  }
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      {nodes.map((node) => (
        <NodeCard key={node.node_id} node={node} live={liveness.get(node.node_id)} />
      ))}
    </div>
  )
}

/**
 * NodesPage lists every household and node on the install (S13: the operator manages them all,
 * through auth's /superuser views), with cc's liveness when available. Train Adapter is gone
 * with LoRA (PLAN §7).
 */
export default function NodesPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useNodesView()
  const { data: liveness } = useNodeLiveness()

  const liveMap = useMemo(() => new Map((liveness ?? []).map((n) => [n.node_id, n])), [liveness])

  const grouped = useMemo(() => {
    if (!data) return { byHousehold: new Map<string, AdminNode[]>(), orphans: [] as AdminNode[] }
    const known = new Set(data.households.map((h) => h.id))
    const byHousehold = new Map<string, AdminNode[]>()
    const orphans: AdminNode[] = []
    for (const n of data.nodes) {
      if (n.household_id && known.has(n.household_id)) {
        byHousehold.set(n.household_id, [...(byHousehold.get(n.household_id) ?? []), n])
      } else {
        orphans.push(n)
      }
    }
    return { byHousehold, orphans }
  }, [data])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <RefreshCw className="animate-spin text-[var(--color-primary)]" size={24} />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="py-20 text-center">
        <p className="mb-2 text-red-500">Failed to load nodes</p>
        <p className="mb-4 text-sm text-[var(--color-text-muted)]">{errorMessage(error, 'Unknown error')}</p>
        <button
          onClick={() => refetch()}
          className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
        >
          Retry
        </button>
      </div>
    )
  }

  const households = data?.households ?? []

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Nodes</h1>
        <button
          onClick={() => refetch()}
          disabled={isFetching}
          className={cn('rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]', isFetching && 'animate-spin')}
          title="Refresh"
        >
          <RefreshCw size={14} className="text-[var(--color-text-muted)]" />
        </button>
      </div>

      {households.length === 0 && grouped.orphans.length === 0 && (
        <div className="py-12 text-center">
          <p className="text-sm text-[var(--color-text-muted)]">
            No households or nodes yet. Add a node by QR provisioning in the mobile app.
          </p>
        </div>
      )}

      {households.map((h) => (
        <div key={h.id} className="space-y-2">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">{h.name}</h2>
          <NodeGrid nodes={grouped.byHousehold.get(h.id) ?? []} liveness={liveMap} />
        </div>
      ))}

      {grouped.orphans.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
            No household
          </h2>
          <NodeGrid nodes={grouped.orphans} liveness={liveMap} />
        </div>
      )}
    </div>
  )
}
