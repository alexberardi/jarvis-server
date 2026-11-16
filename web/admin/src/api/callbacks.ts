import { apiClient } from './client'

/**
 * Inbox card taps (docs/webapp/03 step 2): a tapped element becomes a callback job in cc. Node-plane
 * elements run on the item's node; server-plane ones (`target: "server"`) run in cc for the
 * item's household. Same contract as jarvis-node-mobile's commandCenterApi.
 */
const CC = '/api/cc/api/v0'

export type NavigationType = 'stack' | 'new_notification' | 'popover'

export interface InteractiveElement {
  /** Unique within the item. */
  id: string
  label: string
  sublabel?: string
  /** A visual category: actor, movie, director, ... */
  kind?: string
  command: string
  callback: string
  data: Record<string, unknown>
  navigation_type?: NavigationType
  /** Absent or "node": the node plane; "server": cc runs it. */
  target?: 'node' | 'server'
}

export interface CallbackRequest {
  command_name: string
  callback_name: string
  data: Record<string, unknown>
  /** Exactly one of target_node_id (node plane) and household_id (server plane). */
  target_node_id?: string
  household_id?: string
  navigation_type?: NavigationType
}

export interface CallbackStatus {
  id: string
  status: 'pending' | 'completed' | 'failed' | 'expired'
  navigation_type: NavigationType
  completed_at: string | null
  error_message: string | null
  context_data: Record<string, unknown> | null
}

export async function sendCallback(req: CallbackRequest): Promise<{ id: string; status: string }> {
  const { data } = await apiClient.post<{ id: string; status: string }>(`${CC}/callbacks`, req)
  return data
}

export async function callbackStatus(jobId: string): Promise<CallbackStatus> {
  const { data } = await apiClient.get<CallbackStatus>(`${CC}/callbacks/${encodeURIComponent(jobId)}/status`)
  return data
}

/**
 * normalizeInteractiveElements keeps the well-formed elements of metadata.interactive_elements and
 * gives each a unique id (as mobile does: command:callback:index when it has none).
 */
export function normalizeInteractiveElements(raw: unknown): InteractiveElement[] {
  if (!Array.isArray(raw)) return []
  const seen = new Set<string>()
  const out: InteractiveElement[] = []
  raw.forEach((entry, index) => {
    if (!entry || typeof entry !== 'object') return
    const el = entry as Partial<InteractiveElement>
    if (typeof el.label !== 'string' || typeof el.command !== 'string' || typeof el.callback !== 'string') return
    let id = typeof el.id === 'string' && el.id ? el.id : `${el.command}:${el.callback}:${index}`
    if (seen.has(id)) id = `${id}#${index}`
    seen.add(id)
    out.push({ ...(el as InteractiveElement), id, data: el.data && typeof el.data === 'object' ? el.data : {} })
  })
  return out
}

/** failureMessage is what went wrong with a job: the server's card if it sent one, else its error. */
export function failureMessage(s: CallbackStatus): string {
  const inbox = (s.context_data?.inbox ?? null) as { title?: unknown; summary?: unknown } | null
  const title = typeof inbox?.title === 'string' ? inbox.title : ''
  const summary = typeof inbox?.summary === 'string' ? inbox.summary : ''
  if (summary) return title ? `${title}: ${summary}` : summary
  if (s.error_message) return s.error_message
  return s.status === 'expired' ? "That didn't run in time. Try again." : "That didn't work."
}
