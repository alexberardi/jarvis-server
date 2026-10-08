import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import * as authApi from '@/api/auth'
import type { SetupJob, SetupState } from '@/api/auth'
import ModelBanner from './ModelBanner'

vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), getSetupState: vi.fn() }))

const auth = vi.mocked(authApi)

const LABEL: Record<string, SetupJob['label']> = { llm: 'live', stt: 'stt', voice: 'tts', speaker: 'speaker', memory: 'embeddings' }
function jobs(states: Record<string, string>, over: Record<string, Partial<SetupJob>> = {}): SetupJob[] {
  return ['llm', 'stt', 'voice', 'speaker', 'memory'].map((j) => ({
    job: j,
    label: LABEL[j],
    labels: [LABEL[j]],
    required: ['llm', 'stt', 'voice'].includes(j),
    state: states[j] ?? 'missing',
    label_state: 'not_configured',
    ...over[j],
  }))
}

function state(over: Partial<SetupState>): SetupState {
  return {
    needs_superuser: false,
    setup_token_required: false,
    version: 'v1',
    superuser: true,
    setup_completed: true,
    setup_step: '',
    models_configured: true,
    live_ready: true,
    labels: { live: 'ready' },
    ...over,
  }
}

function renderBanner() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ModelBanner />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('dashboard model banner (AD3b)', () => {
  it('names the voice pieces that are missing and links to the first one', async () => {
    auth.getSetupState.mockResolvedValue(state({ jobs: jobs({ llm: 'ready', stt: 'failed', voice: 'missing', speaker: 'missing' }) }))
    renderBanner()
    expect(await screen.findByText("Voice isn't ready: Speech-to-text and Voice are missing")).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Set up models' })).toHaveAttribute('href', '/models?job=stt')
  })

  it('names a single missing piece', async () => {
    auth.getSetupState.mockResolvedValue(state({ jobs: jobs({ llm: 'missing', stt: 'ready', voice: 'ready' }) }))
    renderBanner()
    expect(await screen.findByText("Voice isn't ready: Language model is missing")).toBeInTheDocument()
  })

  it('shows downloads in progress when nothing required is missing', async () => {
    const install = { id: 1, model_id: 'qwen3-4b', state: 'running', phase: 'model', bytes_done: 30, bytes_total: 100 }
    auth.getSetupState.mockResolvedValue(
      state({ jobs: jobs({ llm: 'downloading', stt: 'ready', voice: 'ready', memory: 'downloading' }, { llm: { install } }) }),
    )
    renderBanner()
    expect(await screen.findByText(/Language model: downloading 30% · Memory & search: downloading/)).toBeInTheDocument()
  })

  it('says nothing when voice has everything (optional jobs skipped)', async () => {
    auth.getSetupState.mockResolvedValue(state({ jobs: jobs({ llm: 'ready', stt: 'ready', voice: 'ready' }) }))
    renderBanner()
    await waitFor(() => expect(auth.getSetupState).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('still sends an unfinished wizard back to setup first', async () => {
    auth.getSetupState.mockResolvedValue(state({ setup_completed: false, setup_step: 'stt', jobs: jobs({}) }))
    renderBanner()
    expect(await screen.findByRole('link', { name: 'Finish setup' })).toHaveAttribute('href', '/setup')
  })
})
