import type { ChatClient } from '@/api/chat'
import type { NodeLiveness } from '@/api/nodes'

export interface DefaultNodeInput {
  /** The household's nodes, in cc's order. */
  nodes: NodeLiveness[]
  /** The household's primary node (smart_home.primary_node_id), "" when unset. */
  primaryId: string
  /** This computer's agent, when the server could tell ("" when it couldn't or there is none). */
  thisDeviceId: string
  /** The node the user picked last time in this household, "" when none. */
  rememberedId: string
}

export type DefaultReason = 'remembered' | 'this-device' | 'primary' | 'first-online' | 'offline-primary' | 'offline-first' | 'none'

/**
 * pickDefaultNode is chat's default node (webapp 01 §0 and §3.3):
 *
 *  0. a remembered pick that is still online (the user chose it);
 *  1. this computer's agent, when the server matched one;
 *  2. the primary node, if online;
 *  3. the first online node;
 *  4. nothing online: the primary, else the first (mobile's tail), shown as offline.
 */
export function pickDefaultNode({ nodes, primaryId, thisDeviceId, rememberedId }: DefaultNodeInput): {
  node: NodeLiveness | null
  reason: DefaultReason
} {
  const byId = (id: string) => (id ? nodes.find((n) => n.node_id === id) : undefined)
  const remembered = byId(rememberedId)
  if (remembered?.online) return { node: remembered, reason: 'remembered' }
  const here = byId(thisDeviceId)
  if (here) return { node: here, reason: 'this-device' }
  const primary = byId(primaryId)
  if (primary?.online) return { node: primary, reason: 'primary' }
  const online = nodes.find((n) => n.online)
  if (online) return { node: online, reason: 'first-online' }
  if (primary) return { node: primary, reason: 'offline-primary' }
  if (nodes[0]) return { node: nodes[0], reason: 'offline-first' }
  return { node: null, reason: 'none' }
}

/**
 * chatClient is a chat turn's `client` (webapp 01 §3.6): same_device when the chosen node is
 * this computer's agent (GET /api/addons/this-device), so cc tells the model the chat page is the
 * window in front and to name the app it types into.
 */
export function chatClient(nodeId: string, thisDeviceId: string): ChatClient {
  return { surface: 'web', same_device: !!nodeId && nodeId === thisDeviceId }
}

/**
 * nodeLabel names a node in the picker and the banner. A computer is named by its registered name
 * (the agent's computer name), since every agent's room is "desktop"; then the room, else a short
 * id. A Pi keeps its room. Mobile's nodeDisplayName follows the same rule.
 */
export function nodeLabel(n: Pick<NodeLiveness, 'node_id' | 'room' | 'name' | 'node_kind'>): string {
  const name = n.node_kind === 'desktop' ? n.name?.trim() : ''
  if (name) return name
  const room = n.room?.trim()
  if (room && room !== 'default') return room.replace(/_/g, ' ')
  return n.node_id.slice(0, 8)
}
