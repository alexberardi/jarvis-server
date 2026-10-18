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
  /** What the node said it is: "desktop" for the desktop agent, null when it never said (a Pi). */
  node_kind?: string | null
  /** What the node reported, e.g. browser_extension: "connected" | "disconnected" | "disabled". */
  capabilities?: Record<string, string | boolean | number>
  /** Who paired it (the approver of its code, or who signed in on it); null when unknown. */
  owner_user_id?: number | null
  /** Its registered name (a desktop agent's computer name); null when it was registered without one. */
  name?: string | null
}

/** extensionState is a desktop agent's browser-extension row, or null when it reported none. */
export function extensionState(n: NodeLiveness | undefined): 'connected' | 'disconnected' | 'disabled' | null {
  const v = n?.capabilities?.browser_extension
  return v === 'connected' || v === 'disconnected' || v === 'disabled' ? v : null
}

/**
 * signOutDevice signs a device out (decision 2026-10-14): cc removes the node and revokes its
 * credentials. Only the member who paired it, or a household admin, may; cc checks. A desktop
 * agent gets no factory reset: it goes back to its setup page with its settings kept.
 */
export async function signOutDevice(nodeId: string): Promise<void> {
  await apiClient.post(`/api/cc/api/v0/nodes/${encodeURIComponent(nodeId)}/sign-out`)
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

/** PairingHousehold is one of the caller's own households, the only ones they can approve into. */
export interface PairingHousehold {
  id: string
  name: string
  role: string
}

/**
 * PairingsView is what the approval form needs: how many computers wait and the caller's
 * households. cc never lists the codes (loop #100): the approver types the one the computer
 * shows, so only someone who can see that computer can approve it.
 */
export interface PairingsView {
  waiting: number
  households: PairingHousehold[]
}

/** getPairings counts the computers waiting for approval, and lists the caller's households. */
export async function getPairings(): Promise<PairingsView> {
  const { data } = await apiClient.get<PairingsView>('/api/cc/api/v0/nodes/pairings')
  return data
}

export interface ApprovePairing {
  code: string
  household_id: string
  room?: string
}

export async function approvePairing(body: ApprovePairing): Promise<void> {
  await apiClient.post('/api/cc/api/v0/nodes/pairings/approve', body)
}

export async function denyPairing(code: string): Promise<void> {
  await apiClient.post('/api/cc/api/v0/nodes/pairings/deny', { code })
}
