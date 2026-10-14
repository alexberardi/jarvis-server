import type { ChatAction, ChatActionContext, ChatEvent, ChatValidation } from '@/api/chat'

/** One bubble. `status` is a transient line ("Starting conversation...") until text arrives. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant'
  text: string
  /** The assistant turn is still streaming. */
  pending?: boolean
  /** The latest status line while nothing has been said yet. */
  status?: string
  /** The model's thinking (cc's `reasoning`, or a `<think>` block in the text). */
  reasoning?: string
  actions?: ChatAction[]
  actionContext?: ChatActionContext
  validation?: ChatValidation
  error?: boolean
  /** How many photos the user attached (the photos themselves are never stored). */
  images?: number
  /** What a pressed action button said back ("Done!", "Action failed."). */
  actionDone?: string
}

export interface ChatState {
  nodeId: string
  conversationId: string
  messages: ChatMessage[]
}

export const emptyChat = (nodeId = ''): ChatState => ({ nodeId, conversationId: '', messages: [] })

/** splitThink pulls `<think>…</think>` out of an answer: some models put their reasoning inline. */
export function splitThink(text: string): { text: string; reasoning: string } {
  const re = /<think>([\s\S]*?)(<\/think>|$)/g
  const parts: string[] = []
  const rest = text.replace(re, (_, inner: string) => {
    parts.push(inner.trim())
    return ''
  })
  return { text: rest.trim(), reasoning: parts.filter(Boolean).join('\n\n') }
}

/**
 * applyEvent folds one stream event into the turn's assistant bubble (the last message).
 * Deltas append (the D32 word replay); `done.full_text` replaces the bubble, which also drops
 * an intermediate "Let me check…" delta; `error` shows the message. `conversation_id` is kept
 * from `done` and `error`, so the next turn continues (or re-warms) the same conversation.
 */
export function applyEvent(state: ChatState, ev: ChatEvent): ChatState {
  const last = state.messages[state.messages.length - 1]
  if (!last || last.role !== 'assistant' || !last.pending) return state
  const replace = (m: ChatMessage): ChatState => ({ ...state, messages: [...state.messages.slice(0, -1), m] })
  switch (ev.type) {
    case 'status':
      return last.text ? state : replace({ ...last, status: ev.message ?? '' })
    case 'acknowledgment':
      // An instant "On it" while the pipeline runs: shown as the status line, never kept.
      return last.text ? state : replace({ ...last, status: ev.text ?? '' })
    case 'delta':
      return replace({ ...last, text: last.text + (ev.text ?? ''), status: undefined })
    case 'done': {
      const { text, reasoning } = splitThink(ev.full_text ?? last.text)
      const next: ChatMessage = {
        ...last,
        text,
        pending: false,
        status: undefined,
        reasoning: ev.reasoning || reasoning || undefined,
        actions: ev.actions?.length ? ev.actions : undefined,
        actionContext: ev.actions?.length ? ev.action_context : undefined,
        validation: ev.stop_reason === 'validation' && ev.validation ? ev.validation : undefined,
      }
      return { ...replace(next), conversationId: ev.conversation_id || state.conversationId }
    }
    case 'error':
      return {
        ...replace({ ...last, text: ev.message || 'Something went wrong.', pending: false, status: undefined, error: true }),
        conversationId: ev.conversation_id || state.conversationId,
      }
  }
  return state
}

/** endTurn closes a turn whose stream ended without done/error (a dropped connection). */
export function endTurn(state: ChatState, message?: string): ChatState {
  const last = state.messages[state.messages.length - 1]
  if (!last || last.role !== 'assistant' || !last.pending) return state
  const text = message ?? (last.text || 'The connection dropped before Jarvis answered.')
  return {
    ...state,
    messages: [...state.messages.slice(0, -1), { ...last, text, pending: false, status: undefined, error: !last.text || !!message }],
  }
}

/** optionLabel is how a validation option reads on its button, and what answering it sends. */
export function optionLabel(o: unknown): string {
  if (typeof o === 'string') return o
  if (typeof o === 'number' || typeof o === 'boolean') return String(o)
  if (o && typeof o === 'object') {
    const r = o as Record<string, unknown>
    for (const k of ['label', 'name', 'title', 'value', 'id']) {
      if (typeof r[k] === 'string' || typeof r[k] === 'number') return String(r[k])
    }
  }
  return JSON.stringify(o)
}

/** chatKey is where a tab keeps its conversation: per user and household (webapp 01 §3.2, G13). */
export function chatKey(userId: number | string, householdId: string): string {
  return `jarvis-admin:chat:${userId}:${householdId}`
}

export function loadChat(key: string): ChatState | null {
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    const s = JSON.parse(raw) as ChatState
    if (typeof s.nodeId !== 'string' || !Array.isArray(s.messages)) return null
    // A turn that was streaming when the tab reloaded will never finish.
    return endTurn(s)
  } catch {
    return null
  }
}

export function saveChat(key: string, s: ChatState): void {
  try {
    sessionStorage.setItem(key, JSON.stringify(s))
  } catch {
    // Storage full or blocked: the conversation just won't survive a reload.
  }
}
