import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import HardwarePanel from './HardwarePanel'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/llm', async (orig) => ({
  ...(await orig<typeof import('@/api/llm')>()),
  getHardware: vi.fn(),
  retryEngine: vi.fn(),
}))

const api = vi.mocked(llm)

const engine: llm.EngineInstance = {
  name: 'llama-cuda-1',
  state: 'failed',
  pid: 0,
  restarts: 2,
  since: '2026-10-17T10:00:00Z',
  last_error: 'exit status 1',
  retries: 3,
  gave_up: false,
  output: null,
  kind: 'llama',
  flavour: 'cuda',
  port: 0,
  model: '/models/bad.gguf',
  labels: ['background'],
  args: null,
}

function hardware(e: llm.EngineInstance): llm.HardwareResponse {
  return {
    hardware: { os: 'linux', arch: 'amd64', devices: [], sources: [], flavour: 'cuda', detected_at: '' },
    proposal: {},
    builds: {},
    installed: [],
    engines: [e],
    voice: [],
  }
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <HardwarePanel />
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('HardwarePanel engines', () => {
  it('shows no Retry while the resolver is still retrying', async () => {
    api.getHardware.mockResolvedValue(hardware(engine))
    renderPanel()
    await screen.findByText('llama-cuda-1')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.queryByText(/gave up/i)).toBeNull()
  })

  it('says it gave up and retries on request', async () => {
    api.getHardware.mockResolvedValue(hardware({ ...engine, gave_up: true }))
    api.retryEngine.mockResolvedValue()
    renderPanel()
    await screen.findByText(/Gave up after 3 retries/)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.retryEngine).toHaveBeenCalledWith('llama-cuda-1'))
    await waitFor(() => expect(api.getHardware).toHaveBeenCalledTimes(2))
  })
})
