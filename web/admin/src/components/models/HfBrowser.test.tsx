import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import type { HfChoice, HfRepoResponse, Install } from '@/api/llm'
import HfBrowser from './HfBrowser'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))

vi.mock('@/api/llm', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/llm')>()
  return {
    ...actual,
    browseRepo: vi.fn(),
    startInstall: vi.fn(),
    hfTokenIsSet: vi.fn(),
  }
})

const api = vi.mocked(llm)

const fit = { verdict: 'fits', needed_mb: 5600, context: 8192, device: 'Apple M3', device_mb: 11468, kv_estimated: true }

function folder(over: Partial<HfChoice> = {}): HfChoice {
  return {
    file: 'config.json',
    kind: 'llm',
    quant: '4bit',
    size: 5_100_000_000,
    shards: [
      { name: 'config.json', size: 2000 },
      { name: 'model.safetensors', size: 5_000_000_000 },
      { name: 'tokenizer.json', size: 10_000 },
    ],
    fit,
    directory: true,
    engine: 'mlx-lm',
    prompt_provider: 'Qwen3_5_9B_Compressed',
    ...over,
  }
}

function repo(files: HfChoice[]): HfRepoResponse {
  return {
    repo: 'mlx-community/Qwen3.5-9B-4bit',
    revision: 'abcdef0123456789',
    gated: false,
    files,
    prompt_providers: ['Qwen3_8B_Compressed', 'Qwen3_14B_Compressed', 'Qwen3_5_9B_Compressed'],
  }
}

function renderBrowser() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <HfBrowser />
    </QueryClientProvider>,
  )
}

async function browse(res: HfRepoResponse) {
  api.browseRepo.mockResolvedValue(res)
  renderBrowser()
  fireEvent.change(screen.getByLabelText('Hugging Face repo'), { target: { value: res.repo } })
  fireEvent.click(screen.getByRole('button', { name: 'Browse' }))
  return screen.findByTestId('hf-config.json')
}

beforeEach(() => {
  vi.clearAllMocks()
  api.hfTokenIsSet.mockResolvedValue(false)
  api.startInstall.mockResolvedValue({ install: { id: 1 } as Install, existing: false })
})

describe('HfBrowser model folders', () => {
  it('lists an MLX folder as one installable model and installs it with its inferred provider', async () => {
    const row = await browse(repo([folder()]))
    expect(within(row).getByText('Model folder')).toBeInTheDocument()
    expect(within(row).getByText('mlx-lm')).toBeInTheDocument()
    expect(within(row).getByText('text-only')).toBeInTheDocument()
    expect(within(row).getByText('3 files')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    expect(within(row).getByText(/Prompt provider: Qwen3_5_9B_Compressed/)).toBeInTheDocument()
    // A text-only engine: no projector to pick.
    expect(within(row).queryByText(/Vision projector/)).not.toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Start install' }))
    await waitFor(() => expect(api.startInstall).toHaveBeenCalled())
    const req = api.startInstall.mock.calls[0][0]
    expect(req).toMatchObject({ repo: 'mlx-community/Qwen3.5-9B-4bit', file: 'config.json', kind: 'llm' })
    expect(req.prompt_provider).toBeUndefined()
    expect(req.mmproj_file).toBeUndefined()
  })

  it('asks for a prompt provider when the folder does not imply one', async () => {
    const row = await browse(repo([folder({ prompt_provider: '' })]))
    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    const start = within(row).getByRole('button', { name: 'Start install' })
    expect(start).toBeDisabled()
    fireEvent.change(within(row).getByLabelText(/Prompt provider/), { target: { value: 'Qwen3_8B_Compressed' } })
    expect(start).toBeEnabled()
    fireEvent.click(start)
    await waitFor(() => expect(api.startInstall).toHaveBeenCalled())
    expect(api.startInstall.mock.calls[0][0].prompt_provider).toBe('Qwen3_8B_Compressed')
  })

  it('says why a folder cannot run here instead of offering the install', async () => {
    const row = await browse(
      repo([folder({ engine: '', unavailable: 'mlx-community/Qwen3.5-9B-4bit is an MLX build, which runs only on Apple Silicon' })]),
    )
    expect(within(row).getByText(/runs only on Apple Silicon/)).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
  })
})
