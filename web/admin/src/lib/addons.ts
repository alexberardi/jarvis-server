import type { NodeLiveness } from '@/api/nodes'

/**
 * Where the Add-ons page sends people (install plan §3.2). A store URL stays null until that
 * listing is public: the page then shows the install guide instead of a dead store link.
 * TODO(install plan 4.2, 3.6): fill in the AMO and Chrome Web Store listings, the App Store and
 * Google Play pages, and per-OS agent installers once they ship.
 */
export const LINKS = {
  releases: 'https://github.com/alexberardi/jarvis-server/releases',
  agentGuide: 'https://github.com/alexberardi/jarvis-server#install-the-agent-from-source',
  browserGuide: 'https://github.com/alexberardi/jarvis-server/blob/main/docs/agent/DESIGN.md#10-browser-control-2026-10-10',
  firefoxStore: null as string | null,
  chromeStore: null as string | null,
  appStore: null as string | null,
  playStore: null as string | null,
}

/** The agent's start link (`jarvis-agent://start`, install plan 1.9): starts an installed, stopped agent. */
export const AGENT_START_URL = 'jarvis-agent://start'

export type DesktopOS = 'macos' | 'windows' | 'linux' | 'other'

/** detectOS reads the browser's platform, for the download button's label. */
export function detectOS(ua: string, platform = ''): DesktopOS {
  const s = `${platform} ${ua}`.toLowerCase()
  if (/iphone|ipad|android/.test(s)) return 'other'
  if (s.includes('mac')) return 'macos'
  if (s.includes('win')) return 'windows'
  if (s.includes('linux') || s.includes('x11')) return 'linux'
  return 'other'
}

export const OS_NAME: Record<DesktopOS, string> = { macos: 'macOS', windows: 'Windows', linux: 'Linux', other: 'your computer' }

/** isDesktopAgent: the node said it is the desktop agent (heartbeat node_kind, install plan 1.8). */
export function isDesktopAgent(n: NodeLiveness): boolean {
  return n.node_kind === 'desktop'
}

/**
 * yourDevices is every desktop agent in the user's own households, sorted online first. The
 * node list is already scoped by cc for a member; a superuser's spans the install, so it is
 * filtered to their memberships here too.
 */
export function yourDevices(nodes: NodeLiveness[], householdIds: string[]): NodeLiveness[] {
  const mine = new Set(householdIds)
  return nodes
    .filter((n) => isDesktopAgent(n) && !!n.household_id && mine.has(n.household_id))
    .sort((a, b) => Number(b.online) - Number(a.online) || a.room.localeCompare(b.room))
}

/** extensionLabel says what a device's browser extension row reads. */
export function extensionLabel(state: 'connected' | 'disconnected' | 'disabled' | null): string {
  switch (state) {
    case 'connected':
      return 'Browser extension connected'
    case 'disconnected':
      return 'Browser extension not connected'
    case 'disabled':
      return 'Browser control turned off'
  }
  return 'Browser extension: not reported'
}

/** The household roles that may sign out anyone's device (cc: admin or higher). */
const SIGN_OUT_ROLES = new Set(['admin', 'owner'])

/**
 * canSignOut mirrors cc's rule, to show the button only where it would work: the member who
 * paired the device, or an admin of its household. The server decides (signout.go); this only
 * hides a button that would answer 403.
 */
export function canSignOut(
  n: NodeLiveness,
  userId: number | undefined,
  households: { id: string; role: string }[],
): boolean {
  const h = households.find((x) => x.id === n.household_id)
  if (!h) return false
  if (userId !== undefined && n.owner_user_id === userId) return true
  return SIGN_OUT_ROLES.has(h.role)
}
