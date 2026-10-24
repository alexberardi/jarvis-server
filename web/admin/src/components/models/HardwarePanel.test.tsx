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

describe('HardwarePanel out of GPU memory (#562)', () => {
  it('says why it gave up at once instead of counting retries, and still offers Retry', async () => {
    const why = "out of GPU memory (cudaMalloc failed: out of memory): the card can't hold this engine next to what else is on it. Free GPU memory, lower the context or pick a smaller model, then press Retry"
    api.getHardware.mockResolvedValue(hardware({ ...engine, retries: 0, restarts: 0, gave_up: true, gave_up_reason: why }))
    renderPanel()
    await screen.findByText(why)
    expect(screen.queryByText(/Gave up after/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })
})

describe('HardwarePanel builds', () => {
  it('offers the MLX engine where it runs and says why not elsewhere', async () => {
    const why = 'mlx-lm needs Apple Silicon (darwin/arm64, macOS 14 or later); this host is linux/amd64'
    api.getHardware.mockResolvedValue({
      ...hardware(engine),
      builds: {
        'llama-server': { build: 'b11457', flavours: ['cpu', 'cuda'] },
        'mlx-lm': { build: '0.32.2', flavours: [], unavailable: why },
      },
    })
    renderPanel()
    await screen.findByText(why)
    expect(screen.getByRole('button', { name: 'Download the cuda build' })).toBeTruthy()

    api.getHardware.mockResolvedValue({
      ...hardware(engine),
      builds: { 'mlx-lm': { build: '0.32.2', flavours: ['metal'] } },
    })
    renderPanel()
    expect(await screen.findByRole('button', { name: 'Download the metal build' })).toBeTruthy()
  })

  it('says what vLLM downloads before it is fetched', async () => {
    api.getHardware.mockResolvedValue({
      ...hardware(engine),
      builds: { vllm: { build: '0.32.0', flavours: ['cuda'], download_bytes: 3_932_507_100, disk_bytes: 17_000_000_000 } },
    })
    renderPanel()
    expect(await screen.findByText('3.7 GB download, needs 15.8 GB free while installing')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Download the cuda build' })).toBeTruthy()
  })

  it('says why vLLM cannot run without an NVIDIA GPU, with no download offered', async () => {
    const why = 'vllm needs an NVIDIA GPU (driver 580 or later); none was detected'
    api.getHardware.mockResolvedValue({
      ...hardware(engine),
      builds: { vllm: { build: '0.32.0', flavours: [], unavailable: why, download_bytes: 3_932_507_100, disk_bytes: 17_000_000_000 } },
    })
    renderPanel()
    await screen.findByText(why)
    expect(screen.queryByText(/free while installing/)).toBeNull()
  })
})
