import { apiClient } from './client'
import { fetchAllHouseholds, fetchAllNodes } from './admin'
import type { AdminHousehold, AdminNode } from './admin'

/** NodeLiveness is cc's view of a node (GET /api/v0/admin/nodes): where it is and when it last spoke. */
export interface NodeLiveness {
  node_id: string
  room: string
  household_id: string | null
  online: boolean
  last_seen: string | null
  last_seen_version: string | null
  is_busy: boolean
}

/** getNodeLiveness lists every active node cc knows (a superuser sees all households). */
export async function getNodeLiveness(): Promise<NodeLiveness[]> {
  const { data } = await apiClient.get<NodeLiveness[]>('/api/cc/api/v0/admin/nodes')
  return data
}

export interface NodesView {
  households: AdminHousehold[]
  nodes: AdminNode[]
}

/**
 * getNodesView is every household and node on the install (S13): the operator manages them all,
 * not only their own households, through auth's /superuser views.
 */
export async function getNodesView(): Promise<NodesView> {
  const [households, nodes] = await Promise.all([fetchAllHouseholds(), fetchAllNodes()])
  return { households, nodes }
}
