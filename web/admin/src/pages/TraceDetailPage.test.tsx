import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { fetchTrace, type TraceDetail } from '@/api/traces'
import { renderPage } from '@/test/renderPage'
import TraceDetailPage from './TraceDetailPage'

vi.mock('@/api/traces', async (orig) => ({
  ...(await orig<typeof import('@/api/traces')>()),
  fetchTrace: vi.fn(),
}))

const at = { path: '/admin/traces/t1', route: '/admin/traces/:id' }

const trace = {
  id: 't1',
  conversation_id: 'conv-1',
  request_type: 'voice',
  source: 'mobile',
  node_id: null,
  household_id: null,
  user_command: 'what time is it',
  assistant_message: 'It is noon.',
  status: 'ok',
  total_duration_ms: 400,
  created_at: '2026-10-12T12:00:00Z',
  error_message: null,
  spans: [
    { name: 'llm.chat', service: 'llm_proxy', start_ms: 0, end_ms: 300, duration_ms: 300, status: 'ok', metadata: {} },
    { name: 'tts.speak', service: 'tts', start_ms: 300, end_ms: 400, duration_ms: 100, status: 'ok', metadata: {} },
  ],
} as unknown as TraceDetail

describe('TraceDetailPage', () => {
  it('heads the page with what was said, and names the source in words, not emoji', async () => {
    vi.mocked(fetchTrace).mockResolvedValue(trace)
    renderPage(<TraceDetailPage />, at)
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('what time is it')
    expect(screen.getByText(/Mobile/)).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/📱|🔊/u)
    expect(screen.getByRole('heading', { name: 'Service flow' })).toBeInTheDocument()
    expect(screen.getByText('LLM Proxy → TTS')).toBeInTheDocument()
  })

  it('says a missing trace is not found, with the way back', async () => {
    vi.mocked(fetchTrace).mockRejectedValue(
      Object.assign(new Error('404'), { isAxiosError: true, response: { status: 404, data: { detail: 'not found' } } }),
    )
    renderPage(<TraceDetailPage />, at)
    expect(await screen.findByRole('alert')).toHaveTextContent('Trace not found')
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Back to traces' })).toBeInTheDocument()
  })

  it('offers a retry when the trace could not be loaded', async () => {
    vi.mocked(fetchTrace).mockRejectedValue(new Error('the server is restarting'))
    renderPage(<TraceDetailPage />, at)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load this trace")
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })
})
