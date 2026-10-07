import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SSEParser } from '@/lib/sse'
import * as client from './client'
import { streamUrl, tailLogs, type LogEntry } from './logs'

function entry(id: number, message = `m${id}`): LogEntry {
  return { id, timestamp: '2026-10-07T12:00:00Z', service: 'cc', level: 'INFO', message, context: null }
}

function sse(...chunks: string[]): Response {
  const enc = new TextEncoder()
  return new Response(
    new ReadableStream({
      start(c) {
        for (const ch of chunks) c.enqueue(enc.encode(ch))
        c.close()
      },
    }),
    { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
  )
}

const ev = (e: LogEntry) => `id: ${e.id}\ndata: ${JSON.stringify(e)}\n\n`

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('jarvis-admin:access_token', 'tok')
  vi.restoreAllMocks()
})

describe('SSEParser', () => {
  it('reassembles events split across chunks and reports comments', () => {
    const comments: string[] = []
    const p = new SSEParser((c) => comments.push(c))
    expect(p.feed(': tail after 7\n\nid: 8\nda')).toEqual([])
    expect(p.feed('ta: {"a":1}\r\n\r\n: ping\n\n')).toEqual([{ id: '8', event: undefined, data: '{"a":1}' }])
    expect(comments).toEqual(['tail after 7', 'ping'])
  })
})

describe('tailLogs (AD9 live tail)', () => {
  it('sends the bearer header and resumes after the last entry it saw', async () => {
    const ctrl = new AbortController()
    const seen: number[] = []
    const urls: string[] = []
    const fetchFn = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      urls.push(String(url))
      expect((init?.headers as Record<string, string>).Authorization).toBe('Bearer tok')
      if (urls.length === 1) return sse(': tail after 41\n\n', ev(entry(42)), ev(entry(43)))
      ctrl.abort()
      return sse()
    })

    await tailLogs({
      filters: { service: 'cc', min_level: 'WARNING' },
      signal: ctrl.signal,
      onEntry: (e) => seen.push(e.id),
      retryMs: 1,
      fetchFn: fetchFn as unknown as typeof fetch,
    })

    expect(seen).toEqual([42, 43])
    expect(urls[0]).toBe('/api/logs/stream?service=cc&min_level=WARNING')
    // The stream dropped: the reconnect resumes after 43, so nothing in between is lost.
    expect(urls[1]).toBe('/api/logs/stream?service=cc&min_level=WARNING&after=43')
  })

  it('resumes from the announced start when the stream drops before any entry', async () => {
    const ctrl = new AbortController()
    const urls: string[] = []
    const fetchFn = vi.fn(async (url: RequestInfo | URL) => {
      urls.push(String(url))
      if (urls.length === 1) return sse(': tail after 99\n\n')
      ctrl.abort()
      return sse()
    })
    await tailLogs({ filters: {}, signal: ctrl.signal, onEntry: () => {}, retryMs: 1, fetchFn: fetchFn as unknown as typeof fetch })
    expect(urls).toEqual(['/api/logs/stream', '/api/logs/stream?after=99'])
  })

  it('starts after a given id, and refreshes the token once on 401', async () => {
    const ctrl = new AbortController()
    const refresh = vi.spyOn(client, 'refreshAccessToken').mockImplementation(async () => {
      localStorage.setItem('jarvis-admin:access_token', 'new')
      return 'new'
    })
    const auths: string[] = []
    const fetchFn = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      auths.push((init?.headers as Record<string, string>).Authorization)
      if (auths.length === 1) return new Response('{}', { status: 401 })
      ctrl.abort()
      return sse(ev(entry(11)))
    })
    const states: string[] = []
    await tailLogs({
      filters: {},
      after: 10,
      signal: ctrl.signal,
      onEntry: () => {},
      onState: (s) => states.push(s),
      retryMs: 1,
      fetchFn: fetchFn as unknown as typeof fetch,
    })
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(auths).toEqual(['Bearer tok', 'Bearer new'])
    expect(fetchFn.mock.calls[0][0]).toBe(streamUrl({}, 10))
    expect(states[0]).toBe('connecting')
    expect(states.at(-1)).toBe('stopped')
  })
})
