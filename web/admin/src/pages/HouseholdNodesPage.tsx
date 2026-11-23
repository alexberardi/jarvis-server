import { useQuery } from '@tanstack/react-query'
import { Cpu } from 'lucide-react'
import { listChatNodes } from '@/api/chat'
import type { AdminNode } from '@/api/admin'
import type { NodeLiveness } from '@/api/nodes'
import PendingPairings from '@/components/nodes/PendingPairings'
import { Card, EmptyState, ErrorState, PageHeader, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { NodeCard } from './NodesPage'

/** asNode is the card's node from cc's liveness: cc lists only active registrations. */
const asNode = (n: NodeLiveness, household: string): AdminNode => ({
  node_id: n.node_id,
  name: n.name || n.room || n.node_id,
  household_id: n.household_id ?? household,
  is_active: true,
  registered_by_user_id: n.owner_user_id ?? null,
  created_at: '',
  updated_at: null,
  last_rotated_at: null,
  services: [],
})

/**
 * HouseholdNodesPage is the active household's voice nodes and computers (mobile's Nodes tab),
 * for every member: whether each is online, its settings, approving a computer's code, and
 * removing one that's offline. cc decides who may change what; the admin's Nodes page is the
 * whole install.
 */
export default function HouseholdNodesPage() {
  const { activeHouseholdId: hh, state } = useAuth()
  const q = useQuery({
    // Under node-liveness, so adding or removing a node anywhere refreshes it.
    queryKey: ['node-liveness', 'household', hh],
    queryFn: () => listChatNodes(hh),
    enabled: !!hh,
    refetchInterval: 30_000,
    retry: false,
  })
  const nodes = [...(q.data ?? [])].sort((a, b) => (a.name || a.room || '').localeCompare(b.name || b.room || ''))

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <PageHeader title="Nodes" description="The household’s voice nodes and computers." />
      {/* /api/system/info is the operator's: anyone else gets the default port in the instructions. */}
      <PendingPairings systemInfo={Boolean(state.user?.is_superuser)} />
      {q.isError ? (
        <Card>
          <ErrorState title="Couldn't load the nodes" message={errorMessage(q.error, 'Check your connection and try again.')} onRetry={() => q.refetch()} />
        </Card>
      ) : q.isLoading ? (
        <Card flush>
          <SkeletonList rows={3} label="Loading nodes" />
        </Card>
      ) : nodes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Cpu size={20} />}
            title="No nodes yet"
            description="Add a voice node with the Jarvis Automation phone app, or install the agent on a computer from Add-ons: its code shows up here to approve."
          />
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {nodes.map((n) => (
            <NodeCard key={n.node_id} node={asNode(n, hh)} live={n} forge={false} data />
          ))}
        </div>
      )}
    </div>
  )
}
