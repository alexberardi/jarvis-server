import { RefreshCw, Cpu, Zap, ZapOff } from 'lucide-react'
import { toast } from 'sonner'
import { useHouseholds, useHouseholdNodes, useTrainAdapter } from '@/hooks/useNodes'
import { cn } from '@/lib/utils'
import type { HouseholdNode } from '@/types/nodes'

function NodeCard({
  node,
  onTrain,
  isTraining,
}: {
  node: HouseholdNode
  onTrain: (nodeId: string) => void
  isTraining: boolean
}) {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-2">
          <Cpu size={16} className="text-[var(--color-text-muted)]" />
          <span className="font-medium text-[var(--color-text)]">{node.name}</span>
        </div>
        <span
          className={cn(
            'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
            node.is_active
              ? 'bg-green-500/10 text-green-500'
              : 'bg-red-500/10 text-red-500',
          )}
        >
          {node.is_active ? <Zap size={10} /> : <ZapOff size={10} />}
          {node.is_active ? 'Active' : 'Inactive'}
        </span>
      </div>

      <div className="mt-3 space-y-1 text-xs text-[var(--color-text-muted)]">
        <p>
          <span className="font-medium">ID:</span> {node.node_id}
        </p>
        {node.services.length > 0 && (
          <p>
            <span className="font-medium">Services:</span> {node.services.join(', ')}
          </p>
        )}
      </div>

      <div className="mt-4">
        <button
          onClick={() => onTrain(node.node_id)}
          disabled={isTraining || !node.is_active}
          className={cn(
            'rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
            isTraining || !node.is_active
              ? 'cursor-not-allowed bg-[var(--color-surface-alt)] text-[var(--color-text-muted)]'
              : 'bg-[var(--color-primary)] text-white hover:opacity-90',
          )}
        >
          {isTraining ? (
            <span className="flex items-center gap-1.5">
              <RefreshCw size={12} className="animate-spin" />
              Training...
            </span>
          ) : (
            'Train Adapter'
          )}
        </button>
      </div>
    </div>
  )
}

function HouseholdSection({ householdId, householdName }: { householdId: string; householdName: string }) {
  const { data: nodes, isLoading, isError } = useHouseholdNodes(householdId)
  const trainMutation = useTrainAdapter()

  const handleTrain = (nodeId: string) => {
    trainMutation.mutate(nodeId, {
      onSuccess: (data) =>
        toast.success(`Training triggered for ${nodeId} (request: ${data.request_id.slice(0, 8)}...)`),
      onError: (err) => toast.error(`Training failed: ${err.message}`),
    })
  }

  return (
    <div className="space-y-2">
      <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
        {householdName}
      </h2>

      {isLoading && (
        <div className="flex items-center justify-center py-8">
          <RefreshCw className="animate-spin text-[var(--color-primary)]" size={18} />
        </div>
      )}

      {isError && (
        <p className="py-4 text-center text-sm text-red-500">Failed to load nodes</p>
      )}

      {nodes && nodes.length === 0 && (
        <p className="py-4 text-center text-sm text-[var(--color-text-muted)]">
          No nodes registered in this household
        </p>
      )}

      {nodes && nodes.length > 0 && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {nodes.map((node) => (
            <NodeCard
              key={node.node_id}
              node={node}
              onTrain={handleTrain}
              isTraining={trainMutation.isPending && trainMutation.variables === node.node_id}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export default function NodesPage() {
  const { data: households, isLoading, isError, error, refetch, isFetching } = useHouseholds()

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
        <p className="mb-2 text-red-500">Failed to load households</p>
        <p className="mb-4 text-sm text-[var(--color-text-muted)]">
          {(error as Error)?.message ?? 'Unknown error'}
        </p>
        <button
          onClick={() => refetch()}
          className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Nodes</h1>
        <button
          onClick={() => refetch()}
          disabled={isFetching}
          className={cn(
            'rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]',
            isFetching && 'animate-spin',
          )}
          title="Refresh"
        >
          <RefreshCw size={14} className="text-[var(--color-text-muted)]" />
        </button>
      </div>

      {(!households || households.length === 0) && (
        <div className="py-12 text-center">
          <p className="text-sm text-[var(--color-text-muted)]">
            No households found. Register a household in jarvis-auth first.
          </p>
        </div>
      )}

      {households?.map((h) => (
        <HouseholdSection key={h.id} householdId={h.id} householdName={h.name} />
      ))}
    </div>
  )
}
