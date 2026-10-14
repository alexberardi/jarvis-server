import { describe, expect, it, vi } from 'vitest'
import { ChatHTTPError, isPasswordChange, streamChat } from './chat'
import type { ChatEvent } from './chat'

const client = vi.hoisted(() => ({ refresh: vi.fn(), token: 'tok' }))
vi.mock('./client', async (orig) => ({
  ...(await orig<typeof import('./client')>()),
  accessToken: () => client.token,
  refreshAccessToken: client.refresh,
}))

/** A response whose body arrives in the given chunks (events split across them on purpose). */
function sse(chunks: string[], status = 200): Response {
  const enc = new TextEncoder()
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      chunks.forEach((ch) => c.enqueue(enc.encode(ch)))
      c.close()
    },
  })
  return new Response(body, { status, headers: { 'Content-Type': 'text/event-stream' } })
}

const req = { message: 'hi', node_id: 'n', household_id: 'h', timezone: 'UTC', include_reasoning: true }

describe('streamChat', () => {
  it('parses events split across network chunks (jarvis-web dropped these)', async () => {
    const got: ChatEvent[] = []
    const fetchFn = vi.fn().mockResolvedValue(
      sse(['data: {"type":"del', 'ta","text":"Hel"}\n', '\ndata: {"type":"done","full_text":"Hello"}\n\n']),
    )
    await streamChat(req, (e) => got.push(e), undefined, fetchFn)
    expect(got.map((e) => e.type)).toEqual(['delta', 'done'])
    const [url, init] = fetchFn.mock.calls[0]
    expect(url).toBe('/api/cc/api/v0/mobile/chat')
    expect(init.headers.Authorization).toBe('Bearer tok')
    expect(JSON.parse(init.body)).toMatchObject({ node_id: 'n', household_id: 'h', include_reasoning: true })
  })

  it('refreshes once on 401 and retries', async () => {
    client.refresh.mockResolvedValue('new')
    const fetchFn = vi
      .fn()
      .mockResolvedValueOnce(new Response('{}', { status: 401 }))
      .mockResolvedValueOnce(sse(['data: {"type":"done","full_text":"ok"}\n\n']))
    const got: ChatEvent[] = []
    await streamChat(req, (e) => got.push(e), undefined, fetchFn)
    expect(fetchFn).toHaveBeenCalledTimes(2)
    expect(got[0].full_text).toBe('ok')
  })

  it('throws the refusal with its detail and code', async () => {
    const fetchFn = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ detail: 'Password change required', code: 'password_change_required' }), { status: 403 }),
    )
    const err = await streamChat(req, () => {}, undefined, fetchFn).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ChatHTTPError)
    expect(isPasswordChange(err)).toBe(true)
    const nf = await streamChat(req, () => {}, undefined, vi.fn().mockResolvedValue(new Response('{"detail":"Node not found"}', { status: 404 }))).catch(
      (e: unknown) => e,
    )
    expect((nf as Error).message).toBe('Node not found')
    expect(isPasswordChange(nf)).toBe(false)
  })
})
