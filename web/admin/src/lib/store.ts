import type { NodeLiveness } from '@/api/nodes'
import type { StoreDetail } from '@/api/store'
import { nodeLabel } from '@/lib/chatNode'

/** parseBundle reads the default bundle setting: comma-separated package names. */
export function parseBundle(v: unknown): string[] {
  return typeof v === 'string' ? v.split(',').filter(Boolean) : []
}

/** withBundle is the bundle with name added or removed, as the setting stores it. */
export function withBundle(bundle: string[], name: string, on: boolean): string {
  const rest = bundle.filter((n) => n !== name)
  return (on ? [...rest, name] : rest).join(',')
}

/**
 * notForComputer says why a computer (desktop agent) can't run a package, or null. It mirrors
 * cc's piOnlyReason, so the dialog leaves such a computer out rather than letting cc refuse it.
 */
export function notForComputer(d: StoreDetail): string | null {
  const label = d.display_name || d.command_name
  if (d.targets?.length && !d.targets.includes('desktop')) return `${label} is made for Pi nodes, not computers`
  if (d.apt_packages?.length) return `${label} needs system packages that only a Pi node installs`
  return null
}

/** aptPackages are the system packages a Pi installs as root for these packages, deduplicated. */
export function aptPackages(pkgs: StoreDetail[]): string[] {
  return [...new Set(pkgs.flatMap((p) => p.apt_packages ?? []))]
}

/** nodeName is what the Store calls a node: its registered name, else the app's label (room or short id). */
export function nodeName(n: NodeLiveness): string {
  return n.name?.trim() || nodeLabel(n)
}

/** nodeRoom is a node's room as a second line, or null when it adds nothing (no room, or it is the name). */
export function nodeRoom(n: NodeLiveness): string | null {
  const room = n.room?.trim().replace(/_/g, ' ')
  return room && room !== 'default' && room !== 'desktop' && room !== nodeName(n) ? room : null
}

/** nodeNames names each node (nodeName), adding its short id where two would read the same. */
export function nodeNames(nodes: NodeLiveness[]): Map<string, string> {
  const count = new Map<string, number>()
  for (const n of nodes) count.set(nodeName(n), (count.get(nodeName(n)) ?? 0) + 1)
  return new Map(
    nodes.map((n) => {
      const l = nodeName(n)
      return [n.node_id, (count.get(l) ?? 0) > 1 ? `${l} (${n.node_id.slice(0, 8)})` : l]
    }),
  )
}
