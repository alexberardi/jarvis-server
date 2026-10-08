import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import * as authApi from '@/api/auth'
import type { SetupJob } from '@/api/auth'
import * as llm from '@/api/llm'
import type { CatalogEntry } from '@/api/llm'
import ModelsPage from './ModelsPage'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), getSetupState: vi.fn() }))
vi.mock('@/api/llm', async (orig) => ({
  ...(await orig<typeof import('@/api/llm')>()),
  getCatalog: vi.fn(),
  getLabels: vi.fn(),
  listInstalls: vi.fn(),
  listInstalled: vi.fn(),
  getHardware: vi.fn(),
  getPromptProvider: vi.fn(),
  hfTokenIsSet: vi.fn(),
}))

const auth = vi.mocked(authApi)
const api = vi.mocked(llm)

const fit = { verdict: 'fits', needed_mb: 1, context: 0, kv_estimated: false }
const entry = (id: string, kind: string): CatalogEntry => ({ id, display: id, kind, file: id, size: 1, sha256: '', fit, installed: false })

const LABEL: Record<string, SetupJob['label']> = { llm: 'live', stt: 'stt', voice: 'tts', speaker: 'speaker', memory: 'embeddings' }
const STATES: Record<string, string> = { llm: 'ready', stt: 'missing', voice: 'downloading', speaker: 'missing', memory: 'ready' }
const jobs: SetupJob[] = Object.keys(LABEL).map((j) => ({
  job: j,
  label: LABEL[j],
  labels: [LABEL[j]],
  required: ['llm', 'stt', 'voice'].includes(j),
  state: STATES[j],
  label_state: 'x',
}))

function renderPage(url = '/models') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <ModelsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.getSetupState.mockResolvedValue({
    needs_superuser: false,
    setup_token_required: false,
    version: 'v1',
    superuser: true,
    jobs,
  })
  api.getCatalog.mockResolvedValue({
    models: [entry('qwen3-4b', 'llm'), entry('whisper-base.en', 'stt'), entry('kokoro', 'tts'), entry('eres2net', 'speaker')],
    recommended: { live: 'qwen3-4b', stt: 'whisper-base.en', speaker: 'eres2net' },
    hardware: { os: 'linux', arch: 'amd64', devices: [], sources: [], flavour: 'cpu', detected_at: '' },
    residents: null,
  })
  api.getLabels.mockResolvedValue({ labels: [], voice: [], engines: [], proposal: {}, recommend: {}, warnings: null })
  api.listInstalls.mockResolvedValue([])
  api.listInstalled.mockResolvedValue({ models: [], disk_bytes: 0, dir: '/m' })
  api.getHardware.mockRejectedValue(new Error('no'))
  api.getPromptProvider.mockResolvedValue({ value: '', derived: '', effective: '', source: '', valid: false, options: [] })
  api.hfTokenIsSet.mockResolvedValue(false)
})

describe('Models page job checklist (AD3b)', () => {
  it('lists every job above the catalog and offers "Set up" only where nothing is on the way', async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Model jobs' })
    expect(within(list).getByTestId('job-stt')).toHaveTextContent('Not set up')
    expect(within(list).getByTestId('job-voice')).toHaveTextContent('Downloading')
    expect(screen.getByRole('button', { name: 'Set up Speech-to-text' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Set up Voice recognition' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set up Language model' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Set up Voice' })).toBeNull()
  })

  it('"Set up" narrows the catalog to that job, and "Show all" widens it again', async () => {
    renderPage()
    expect(await screen.findByTestId('catalog-qwen3-4b')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Set up Speech-to-text' }))
    expect(await screen.findByText(/Showing models for/)).toHaveTextContent('Showing models for Speech-to-text')
    expect(screen.getByTestId('catalog-whisper-base.en')).toBeInTheDocument()
    expect(screen.queryByTestId('catalog-qwen3-4b')).toBeNull()
    // "Install recommended" narrows with it: just the STT recommendation.
    expect(screen.getByRole('button', { name: /Install recommended \(1\)/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Show all/ }))
    expect(await screen.findByTestId('catalog-qwen3-4b')).toBeInTheDocument()
  })

  it('opens filtered from the dashboard link', async () => {
    renderPage('/models?job=speaker')
    expect(await screen.findByTestId('catalog-eres2net')).toBeInTheDocument()
    expect(screen.queryByTestId('catalog-whisper-base.en')).toBeNull()
  })
})
