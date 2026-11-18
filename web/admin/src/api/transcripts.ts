import { apiClient } from './client'

/**
 * Recent commands (mobile's Recent Commands): the caller's own turns, newest first, what they
 * said, the tool calls the model made and its reply. cc selects by the caller's user id.
 */
/** One command a node ran: name and parsed arguments (see toolCalls). */
export interface ToolCall {
  name: string
  arguments: Record<string, unknown>
}

export interface Transcript {
  id: number
  household_id: string
  user_message: string
  assistant_message: string | null
  /**
   * As cc stores them, OpenAI-shaped: {function: {name, arguments: "<JSON>"}}. Only the commands
   * a node ran are recorded (legacy parity), not the ones jarvisd handles itself (remember, …).
   */
  tool_calls: unknown[] | null
  created_at: string
}

export async function listRecentTranscripts(limit = 50): Promise<Transcript[]> {
  const { data } = await apiClient.get<Transcript[]>('/api/cc/api/v0/transcripts/recent', { params: { limit } })
  return data
}

/** toolCalls reads a transcript's stored calls (OpenAI shape, or a flat {name, arguments}). */
export function toolCalls(t: Transcript): ToolCall[] {
  return (t.tool_calls ?? []).flatMap((raw) => {
    const o = (raw ?? {}) as Record<string, unknown>
    const fn = (o.function ?? o) as Record<string, unknown>
    const name = typeof fn.name === 'string' ? fn.name : ''
    if (!name) return []
    let args: unknown = fn.arguments
    if (typeof args === 'string') {
      try {
        args = JSON.parse(args)
      } catch {
        args = { arguments: args }
      }
    }
    return [{ name, arguments: args && typeof args === 'object' ? (args as Record<string, unknown>) : {} }]
  })
}

/** callSummary is one tool call as a line: name(key=value, …), values over 20 characters cut with …. */
export function callSummary(c: ToolCall): string {
  const args = Object.entries(c.arguments)
    .slice(0, 3)
    .map(([k, v]) => {
      const text = typeof v === 'string' ? v : JSON.stringify(v)
      return `${k}=${text.length > 20 ? `${text.slice(0, 19)}…` : text}`
    })
  return args.length ? `${c.name}(${args.join(', ')})` : c.name
}
