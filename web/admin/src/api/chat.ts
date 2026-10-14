import { accessToken, apiClient, isPasswordChangeRequired, refreshAccessToken } from './client'
import type { NodeLiveness } from './nodes'
import { SSEParser } from '@/lib/sse'

/**
 * Chat in the browser (webapp 01 §3): cc's mobile chat, unchanged, through the admin gateway's
 * member pass routes (`/api/cc/...`). A node of another household is 404 there.
 */
const CC = '/api/cc/api/v0'

/** The household's nodes for the picker. Always scoped: a superuser would otherwise see every household's (G9). */
export async function listChatNodes(householdId: string): Promise<NodeLiveness[]> {
  const { data } = await apiClient.get<NodeLiveness[]>(`${CC}/admin/nodes`, { params: { household_id: householdId } })
  return data
}

/** primaryNodeId is the household's primary node (smart_home.primary_node_id), "" when unset. */
export async function getPrimaryNodeId(householdId: string): Promise<string> {
  const { data } = await apiClient.get<{ primary_node_id?: string | null }>(
    `${CC}/households/${encodeURIComponent(householdId)}/smart-home/config`,
  )
  return data.primary_node_id ?? ''
}

export interface ChatCapabilities {
  /** The live model accepts images (cc/chat-images.md §6). */
  images: boolean
  max_images: number
  max_image_bytes: number
}

export async function getChatCapabilities(): Promise<ChatCapabilities> {
  const { data } = await apiClient.get<ChatCapabilities>(`${CC}/mobile/chat/capabilities`)
  return data
}

/** NodeTools is what a node reported it can do, handed to warmup and the first turn. */
export interface NodeTools {
  client_tools: Record<string, unknown>[]
  available_commands: Record<string, unknown>[]
}

export async function getNodeTools(nodeId: string): Promise<NodeTools> {
  const { data } = await apiClient.get<NodeTools>(`${CC}/mobile/nodes/${encodeURIComponent(nodeId)}/tools`)
  return { client_tools: data.client_tools ?? [], available_commands: data.available_commands ?? [] }
}

export interface WarmupRequest {
  node_id: string
  household_id: string
  timezone: string
  client_tools?: Record<string, unknown>[]
  available_commands?: Record<string, unknown>[]
}

export async function warmupChat(req: WarmupRequest): Promise<{ conversation_id: string }> {
  const { data } = await apiClient.post<{ conversation_id: string }>(`${CC}/mobile/chat/warmup`, req)
  return data
}

/** An SDK button under an answer (cc's `done.actions`). */
export interface ChatAction {
  button_text: string
  button_action: string
  button_type?: 'primary' | 'secondary' | 'destructive' | string
  completion_message?: string
}

export interface ChatActionContext {
  command_name: string
  context: Record<string, unknown>
}

export interface ChatValidation {
  question: string
  parameter_name?: string
  options: unknown[]
}

/** One event of the chat stream (cc/13 §3.2). */
export interface ChatEvent {
  type: 'status' | 'acknowledgment' | 'delta' | 'done' | 'error' | string
  text?: string
  message?: string
  conversation_id?: string
  full_text?: string
  stop_reason?: string
  actions?: ChatAction[]
  action_context?: ChatActionContext
  action_preview?: unknown
  validation?: ChatValidation
  reasoning?: string
}

export interface ChatImagePayload {
  mime: string
  data: string
}

export interface ChatTurnRequest {
  message: string
  node_id: string
  household_id: string
  conversation_id?: string
  timezone: string
  include_reasoning: boolean
  client_tools?: Record<string, unknown>[]
  available_commands?: Record<string, unknown>[]
  images?: ChatImagePayload[]
}

/** ChatHTTPError is a turn refused before the stream started (status, and cc's detail). */
export class ChatHTTPError extends Error {
  status: number
  code?: string
  constructor(status: number, message: string, code?: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function errorFrom(res: Response): Promise<ChatHTTPError> {
  let detail = ''
  let code: string | undefined
  try {
    const body = (await res.json()) as { detail?: unknown; code?: unknown }
    if (typeof body.detail === 'string') detail = body.detail
    if (typeof body.code === 'string') code = body.code
  } catch {
    // not JSON
  }
  return new ChatHTTPError(res.status, detail || `Chat failed (${res.status})`, code)
}

/**
 * streamChat sends one turn and hands each event to onEvent. fetch, not EventSource: the
 * request is a POST with a bearer header. A 401 refreshes the token once; a
 * password_change_required 403 is thrown like any other refusal (the axios interceptor's
 * equivalent is triggered by the caller).
 */
export async function streamChat(
  req: ChatTurnRequest,
  onEvent: (e: ChatEvent) => void,
  signal?: AbortSignal,
  fetchFn: typeof fetch = fetch,
): Promise<void> {
  let refreshed = false
  for (;;) {
    const token = accessToken()
    const res = await fetchFn(`${CC}/mobile/chat`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify(req),
      signal,
    })
    if (res.status === 401 && !refreshed) {
      refreshed = true
      if (await refreshAccessToken()) continue
    }
    if (!res.ok) throw await errorFrom(res)
    if (!res.body) return
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    const parser = new SSEParser()
    for (;;) {
      const { done, value } = await reader.read()
      const text = done ? decoder.decode() : decoder.decode(value, { stream: true })
      for (const ev of parser.feed(text + (done ? '\n\n' : ''))) {
        try {
          onEvent(JSON.parse(ev.data) as ChatEvent)
        } catch {
          // a malformed event: skip it
        }
      }
      if (done) return
    }
  }
}

/** isPasswordChange reports a streamChat refusal that means "change your password first". */
export function isPasswordChange(err: unknown): boolean {
  if (err instanceof ChatHTTPError) return err.status === 403 && err.code === 'password_change_required'
  return isPasswordChangeRequired(err)
}

export interface ActionResult {
  status: string
  success: boolean | null
  error?: string | null
}

/** sendNodeAction runs an answer's button on the node (POST /nodes/{id}/actions). */
export async function sendNodeAction(
  nodeId: string,
  body: { command_name: string; action_name: string; context: Record<string, unknown> },
): Promise<ActionResult> {
  const { data } = await apiClient.post<ActionResult>(`${CC}/nodes/${encodeURIComponent(nodeId)}/actions`, body)
  return data
}
