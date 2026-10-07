/**
 * The admin Logs page's backend (AD9, A4 "As built" #7): `/api/logs`, `/api/logs/sources` and
 * the live tail `/api/logs/stream`, an SSE stream read with fetch() so the bearer header can go
 * along (EventSource can't send one, and tokens never go in a query string).
 */
import { accessToken, apiClient, refreshAccessToken } from './client'
import { SSEParser } from '@/lib/sse'

export const LOG_LEVELS = ['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'] as const

export interface LogEntry {
  id: number
  timestamp: string
  service: string
  level: string
  message: string
  context: Record<string, unknown> | null
  node_id?: string
}

export interface LogFilters {
  service?: string
  node_id?: string
  /** That level and worse. */
  min_level?: string
  /** RFC 3339. */
  since?: string
  until?: string
  /** Case-insensitive literal substring of the message or context. */
  q?: string
}

export interface LogPage {
  logs: LogEntry[]
  next_cursor: string | null
}

export interface LogSources {
  services: string[]
  nodes: string[]
  since: string
}

function filterParams(f: LogFilters): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(f)) if (typeof v === 'string' && v.trim() !== '') out[k] = v.trim()
  return out
}

/** getLogs is one page, newest first; pass next_cursor back for the next (older) page. */
export async function getLogs(filters: LogFilters, cursor?: string | null, limit = 200): Promise<LogPage> {
  const params: Record<string, string> = { ...filterParams(filters), limit: String(limit) }
  if (cursor) params.cursor = cursor
  const { data } = await apiClient.get<LogPage>('/api/logs', { params })
  return data
}

export async function getLogSources(): Promise<LogSources> {
  const { data } = await apiClient.get<LogSources>('/api/logs/sources')
  return data
}

export type TailState = 'connecting' | 'live' | 'reconnecting' | 'stopped'

export interface TailOptions {
  filters: LogFilters
  /** Resume after this entry id; without it the tail starts after the newest entry. */
  after?: number
  signal: AbortSignal
  onEntry: (e: LogEntry) => void
  onState?: (s: TailState, detail?: string) => void
  /** Delay before reconnecting after the stream drops. */
  retryMs?: number
  fetchFn?: typeof fetch
}

/** streamUrl builds the tail URL; `after` resumes with no gap. */
export function streamUrl(filters: LogFilters, after?: number): string {
  const p = new URLSearchParams(filterParams(filters))
  if (after !== undefined) p.set('after', String(after))
  const qs = p.toString()
  return `/api/logs/stream${qs ? `?${qs}` : ''}`
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const t = setTimeout(resolve, ms)
    signal.addEventListener('abort', () => {
      clearTimeout(t)
      resolve()
    }, { once: true })
  })
}

/**
 * tailLogs follows the log stream until `signal` aborts. Whenever the stream drops it
 * reconnects with `?after=<last id seen>`, so nothing logged in between is lost. Before the
 * first entry, the id it resumes from is the one the server announces in its opening
 * `: tail after N` comment. A 401 refreshes the access token once and retries.
 */
export async function tailLogs(opts: TailOptions): Promise<void> {
  const { filters, signal, onEntry, onState, retryMs = 2000 } = opts
  const fetchFn = opts.fetchFn ?? fetch
  let last = opts.after
  let refreshed = false
  let attempt = 0

  while (!signal.aborted) {
    onState?.(attempt++ === 0 ? 'connecting' : 'reconnecting')
    let detail: string | undefined
    try {
      const token = accessToken()
      const res = await fetchFn(streamUrl(filters, last), {
        headers: { Accept: 'text/event-stream', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
        signal,
        cache: 'no-store',
      })
      if (res.status === 401 && !refreshed) {
        refreshed = true
        if (await refreshAccessToken()) continue
      }
      if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`)
      refreshed = false
      onState?.('live')
      const parser = new SSEParser((comment) => {
        const m = /^tail after (\d+)/.exec(comment)
        if (m && last === undefined) last = Number(m[1])
      })
      const reader = res.body.getReader()
      const dec = new TextDecoder()
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        for (const ev of parser.feed(dec.decode(value, { stream: true }))) {
          let entry: LogEntry
          try {
            entry = JSON.parse(ev.data) as LogEntry
          } catch {
            continue
          }
          const id = ev.id !== undefined && ev.id !== '' ? Number(ev.id) : entry.id
          if (Number.isFinite(id)) last = id
          onEntry(entry)
        }
      }
      detail = 'stream ended'
    } catch (err) {
      if (signal.aborted) break
      detail = err instanceof Error ? err.message : String(err)
    }
    if (signal.aborted) break
    onState?.('reconnecting', detail)
    await sleep(retryMs, signal)
  }
  onState?.('stopped')
}
