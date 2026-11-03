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
  /**
   * What the node reported, e.g. browser_extension: "connected" | "disconnected" | "disabled",
   * browser_website_access: "on" | "off" (only with an extension connected that says),
   * computer_control: "on" | "off".
   */
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
 * websiteAccessState is whether a desktop agent's connected browser extension may read and act on
 * pages (Firefox lets the user turn it off); null when it didn't say (no extension connected, or one before 0.3.2).
 */
export function websiteAccessState(n: NodeLiveness | undefined): 'on' | 'off' | null {
  const v = n?.capabilities?.browser_website_access
  return v === 'on' || v === 'off' ? v : null
}

/**
 * computerControlState is a computer's "Computer control" switch (COMPUTER-CONTROL.md C11), set in
 * its agent's menu and only shown here: off unless it reported on. Null for a node that isn't a computer.
 */
export function computerControlState(n: NodeLiveness | undefined): 'on' | 'off' | null {
  if (n?.node_kind !== 'desktop') return null
  return n.capabilities?.computer_control === 'on' ? 'on' : 'off'
}

export type LedMode = 'always' | 'responding' | 'off'

/**
 * ledModeState is a voice node's LED mode as it reported it (the ReSpeaker HAT's LEDs on a Pi):
 * always on, lit only while responding, or off. Null for a node without LEDs or an older agent.
 */
export function ledModeState(n: NodeLiveness | undefined): LedMode | null {
  const v = n?.capabilities?.led_mode
  return v === 'always' || v === 'responding' || v === 'off' ? v : null
}

/**
 * setLedMode sends one node its LED mode through cc's node-config relay (the app's own path):
 * led_enabled turns them on or off, and led_idle_enabled says whether the dim idle glow shows.
 * Choosing off leaves the idle choice as it was, so turning them back on restores it.
 */
export async function setLedMode(nodeId: string, mode: LedMode): Promise<void> {
  const settings: Record<string, boolean> = { led_enabled: mode !== 'off' }
  if (mode !== 'off') settings.led_idle_enabled = mode === 'always'
  await apiClient.post(`/api/cc/api/v0/nodes/${encodeURIComponent(nodeId)}/node-config`, { settings, restart: false })
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
