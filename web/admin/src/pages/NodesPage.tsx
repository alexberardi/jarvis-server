import { useMemo, useState, type ReactNode } from 'react'
import { Cpu, Laptop, RefreshCw } from 'lucide-react'
import DisplaySettings from '@/components/display/DisplaySettings'
import PendingPairings from '@/components/nodes/PendingPairings'
import { Badge, Button, Card, EmptyState, ErrorState, PageHeader, Select, SkeletonList, toast } from '@/components/ui'
import { useNodeLiveness, useNodesView } from '@/hooks/useNodes'
import { errorMessage } from '@/lib/errors'
import { formatVersion } from '@/lib/format'
import { timeAgo } from '@/lib/time'
import type { AdminNode } from '@/api/admin'
import { computerControlState, extensionState, ledModeState, setLedMode, websiteAccessState } from '@/api/nodes'
import type { LedMode, NodeLiveness } from '@/api/nodes'

const extensionText = { connected: 'connected', disconnected: 'not connected', disabled: 'off' } as const

/** Detail is one "Label: value" line of a node card. */
function Detail({ label, children, title }: { label: string; children: ReactNode; title?: string }) {
  return (
    <p title={title} className="min-w-0 break-words">
      <span className="text-text-muted">{label}:</span> {children}
    </p>
  )
}

const ledModeOptions = [
  { value: 'always', label: 'Always on' },
  { value: 'responding', label: 'Only when responding' },
  { value: 'off', label: 'Off' },
]

/**
 * LedModeField is one voice node's LED setting: the HAT's LEDs always on, lit only while the node
 * responds (wake, listening, thinking, speaking, errors, alerts), or off. It is per node: it goes
 * to that node alone, and the node keeps it across restarts. The node reports the value back with
 * its next heartbeat, so the choice shows at once and settles on what the node says (the field starts again whenever the reported value changes).
 */
function LedModeField({ nodeId, reported, online }: { nodeId: string; reported: LedMode; online: boolean }) {
  const [mode, setMode] = useState<LedMode>(reported)
  const [saving, setSaving] = useState(false)
  const change = async (next: string) => {
    const previous = mode
    setMode(next as LedMode)
    setSaving(true)
    try {
      await setLedMode(nodeId, next as LedMode)
      toast.success('LED setting sent')
    } catch {
      setMode(previous)
      toast.error("Couldn't send the LED setting to this node")
    } finally {
      setSaving(false)
    }
  }
  return (
    <Select
      label="LEDs"
      hint={online ? undefined : 'The node is offline; it applies the setting when it is back.'}
      options={ledModeOptions}
      value={mode}
      disabled={saving}
      onValueChange={change}
    />
  )
}

function NodeCard({ node, live }: { node: AdminNode; live?: NodeLiveness }) {
  const desktop = live?.node_kind === 'desktop'
  const ext = extensionState(live)
  // Website access off: the browser is connected but Jarvis can't read or act on its pages.
  const accessOff = ext === 'connected' && websiteAccessState(live) === 'off'
  const computerControl = computerControlState(live)
  const ledMode = ledModeState(live)
  const Icon = desktop ? Laptop : Cpu
  return (
    <Card as="article" data-testid={`node-${node.node_id}`}>
      {/* The badges drop below the name when both don't fit, rather than squeezing the name. */}
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Icon size={18} className="shrink-0 text-text-muted" aria-label={desktop ? 'Computer' : 'Node'} />
          <h3 className="min-w-0 text-subheading break-words text-text">{node.name}</h3>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1.5">
          {live && (
            <Badge tone={live.online ? 'success' : 'neutral'} dot={live.online}>
              {live.online ? 'Online' : 'Offline'}
            </Badge>
          )}
          <Badge tone={node.is_active ? 'neutral' : 'warning'}>{node.is_active ? 'Active' : 'Inactive'}</Badge>
        </div>
      </div>

      <div className="mt-3 space-y-1 text-small text-text">
        {live?.room && <Detail label="Room">{live.room}</Detail>}
        {live && (
          <Detail label="Last seen">
            {timeAgo(live.last_seen)}
            {live.last_seen_version && <> &middot; {formatVersion(live.last_seen_version)}</>}
          </Detail>
        )}
        {ext && (
          <Detail label="Browser extension">
            {extensionText[ext]}
            {accessOff && (
              <span className="text-warning" title='Turn it on with "Turn on" in the Jarvis Browser Control toolbar button on that computer'>
                , website access off
              </span>
            )}
          </Detail>
        )}
        {computerControl && (
          <Detail
            label="Computer control"
            title='Turned on and off with "Computer control" in the Jarvis Automation menu on this computer'
          >
            {computerControl}
          </Detail>
        )}
        {ledMode && live && <LedModeField key={ledMode} nodeId={node.node_id} reported={ledMode} online={live.online} />}
        {live?.capabilities?.display === true && <>
          <Detail label="Display">{String(live.capabilities.display_status || (live.capabilities.display_enabled ? 'Ready' : 'Disabled'))}</Detail>
          <Detail label="Playercast receiver">{String(live.capabilities.display_playercast || 'Disabled')}</Detail>
          <Detail label="Experimental Cast V2">{String(live.capabilities.display_cast || 'Disabled')}</Detail>
          <DisplaySettings scope={node.node_id} label="Display settings" />
        </>}
        {node.services.length > 0 && <Detail label="Services">{node.services.join(', ')}</Detail>}
        <Detail label="ID">
          <code className="font-mono break-all text-text-muted">{node.node_id}</code>
        </Detail>
      </div>
    </Card>
  )
}

function NodeGrid({ nodes, liveness }: { nodes: AdminNode[]; liveness: Map<string, NodeLiveness> }) {
  if (nodes.length === 0) {
    return (
      <Card>
        <p className="text-small text-text-muted">No nodes in this household yet.</p>
      </Card>
    )
  }
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
      {nodes.map((node) => (
        <NodeCard key={node.node_id} node={node} live={liveness.get(node.node_id)} />
      ))}
    </div>
  )
}

function NodeGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3" aria-label={title}>
      <h2 className="text-heading text-text">{title}</h2>
      {children}
    </section>
  )
}

/**
 * NodesPage lists every household and node on the install (S13: the operator manages them all,
 * through auth's /superuser views), with cc's liveness when available. Train Adapter is gone
 * with LoRA (PLAN §7). A signed-out registration (auth keeps it inactive after Sign out, Delete,
 * or a computer enrolling again) is hidden until the operator asks for it.
 */
export default function NodesPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useNodesView()
  const { data: liveness } = useNodeLiveness()
  const [showInactive, setShowInactive] = useState(false)

  const liveMap = useMemo(() => new Map((liveness ?? []).map((n) => [n.node_id, n])), [liveness])

  const grouped = useMemo(() => {
    if (!data) return { byHousehold: new Map<string, AdminNode[]>(), orphans: [] as AdminNode[] }
    const known = new Set(data.households.map((h) => h.id))
    const byHousehold = new Map<string, AdminNode[]>()
    const orphans: AdminNode[] = []
    for (const n of data.nodes) {
      if (!n.is_active && !showInactive) continue
      if (n.household_id && known.has(n.household_id)) {
        byHousehold.set(n.household_id, [...(byHousehold.get(n.household_id) ?? []), n])
      } else {
        orphans.push(n)
      }
    }
    return { byHousehold, orphans }
  }, [data, showInactive])
  const inactiveCount = useMemo(() => (data?.nodes ?? []).filter((n) => !n.is_active).length, [data])

  const header = (
    <PageHeader
      title="Nodes"
      description="Voice nodes and computers on this install, by household."
      actions={
        <Button size="sm" icon={<RefreshCw size={16} aria-hidden />} loading={isFetching} onClick={() => refetch()}>
          Refresh
        </Button>
      }
    />
  )

  if (isLoading) {
    return (
      <div className="mx-auto max-w-5xl">
        {header}
        <Card flush>
          <SkeletonList rows={3} label="Loading nodes" />
        </Card>
      </div>
    )
  }

  if (isError) {
    return (
      <div className="mx-auto max-w-5xl">
        {header}
        <Card>
          <ErrorState
            title="Couldn't load the nodes"
            message={errorMessage(error, 'The server did not answer. Check that Jarvis Automation is running.')}
            onRetry={() => refetch()}
          />
        </Card>
      </div>
    )
  }

  const households = data?.households ?? []

  return (
    <div className="mx-auto max-w-5xl">
      {header}
      <div className="space-y-8">
        <PendingPairings />

        {households.length === 0 && grouped.orphans.length === 0 && (
          <Card>
            <EmptyState
              icon={<Cpu size={20} />}
              title="No nodes yet"
              description="Add a voice node with the Jarvis Automation phone app (scan its QR code), or pair a computer: its code shows up under Pending pairings."
            />
          </Card>
        )}

        {households.map((h) => (
          <NodeGroup key={h.id} title={h.name}>
            <NodeGrid nodes={grouped.byHousehold.get(h.id) ?? []} liveness={liveMap} />
          </NodeGroup>
        ))}

        {grouped.orphans.length > 0 && (
          <NodeGroup title="No household">
            <NodeGrid nodes={grouped.orphans} liveness={liveMap} />
          </NodeGroup>
        )}

        {inactiveCount > 0 && (
          <Button variant="ghost" size="sm" onClick={() => setShowInactive((v) => !v)}>
            {showInactive
              ? 'Hide signed-out nodes'
              : `Show ${inactiveCount} signed-out node${inactiveCount === 1 ? '' : 's'}`}
          </Button>
        )}
      </div>
    </div>
  )
}
