import { apiClient } from './client'

/**
 * The inbox (webapp 01 P5): notifications' per-user rows through the gateway's member routes
 * (`/api/notifications/api/v0/inbox*`). Long-running jobs (deep research, confirmations)
 * report back here. Items are the active household's (the access token's).
 */
const INBOX = '/api/notifications/api/v0/inbox'

export interface InboxItem {
  id: string
  user_id: number | null
  household_id: string
  title: string
  summary: string
  body: string
  category: string
  source_service: string
  metadata: Record<string, unknown> | null
  is_read: boolean
  created_at: string
}

export async function listInbox(params?: { limit?: number; is_read?: boolean; category?: string }): Promise<InboxItem[]> {
  const { data } = await apiClient.get<InboxItem[]>(INBOX, { params })
  return data
}

export async function getInboxItem(id: string): Promise<InboxItem> {
  const { data } = await apiClient.get<InboxItem>(`${INBOX}/${encodeURIComponent(id)}`)
  return data
}

export async function unreadCount(): Promise<number> {
  const { data } = await apiClient.get<{ count: number }>(`${INBOX}/unread-count`)
  return data.count
}

export async function markRead(id: string): Promise<void> {
  await apiClient.patch(`${INBOX}/${encodeURIComponent(id)}/read`, {})
}

export async function deleteItem(id: string): Promise<void> {
  await apiClient.delete(`${INBOX}/${encodeURIComponent(id)}`)
}

export async function bulkRead(ids: string[]): Promise<void> {
  if (ids.length) await apiClient.post(`${INBOX}/bulk/read`, { ids })
}

export async function bulkDelete(ids: string[]): Promise<void> {
  if (ids.length) await apiClient.post(`${INBOX}/bulk/delete`, { ids })
}

export interface InboxSource {
  title: string
  url: string
}

/** sourcesOf reads a research item's sources (metadata.sources), keeping only http(s) links. */
export function sourcesOf(item: InboxItem): InboxSource[] {
  const raw = item.metadata?.sources
  if (!Array.isArray(raw)) return []
  return raw.flatMap((s) => {
    const o = s as Record<string, unknown>
    const url = typeof o?.url === 'string' ? o.url : ''
    if (!/^https?:\/\//i.test(url)) return []
    return [{ url, title: typeof o.title === 'string' && o.title ? o.title : url }]
  })
}

export interface InboxAction {
  button_text: string
  button_action: string
  button_type?: string
}

/**
 * confirmationOf is a confirmation item's buttons and the node action they run (jarvis-web's
 * inbox: metadata.command_name + node_id, the draft as context), or null when it has none.
 */
export function confirmationOf(item: InboxItem): { nodeId: string; commandName: string; context: Record<string, unknown>; actions: InboxAction[] } | null {
  const m = item.metadata ?? {}
  const actions = Array.isArray(m.actions) ? (m.actions as InboxAction[]).filter((a) => a && typeof a.button_action === 'string') : []
  const nodeId = typeof m.node_id === 'string' ? m.node_id : ''
  const commandName = typeof m.command_name === 'string' ? m.command_name : ''
  if (!actions.length || !nodeId || !commandName) return null
  const context = m.context && typeof m.context === 'object' ? (m.context as Record<string, unknown>) : { draft: m.draft }
  return { nodeId, commandName, context, actions }
}
