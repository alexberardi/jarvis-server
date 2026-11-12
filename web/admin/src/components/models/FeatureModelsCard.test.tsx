import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import type { AggregatedSettingsResponse } from '@/types/settings'
import FeatureModelsCard from './FeatureModelsCard'
import { FEATURES, cloudProvider, slotSeesImages } from './features'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/llm', async (orig) => ({ ...(await orig<typeof import('@/api/llm')>()), getLabels: vi.fn() }))
vi.mock('@/api/settings', async (orig) => ({
  ...(await orig<typeof import('@/api/settings')>()),
  getServiceSettings: vi.fn(),
  updateSetting: vi.fn(),
}))

const api = vi.mocked(llm)
const settings = vi.mocked(settingsApi)

function cloudConfig(url: string): llm.LabelConfig {
  return {
    label: 'cloud',
    kind: 'llama-server',
    model_kind: 'llm',
    engine: 'remote',
    model: '',
    model_path: '',
    mmproj: '',
    mmproj_path: '',
    context: 0,
    parallel: 0,
    gpu_backend: '',
    gpu_devices: '',
    split_mode: '',
    tensor_split: '',
    gpu_layers: 0,
    kv_cache_type: '',
    flash_attn: '',
    extra_args: '',
    embedding: false,
    remote_url: url,
    remote_model: url ? 'claude-sonnet-5-5' : '',
    remote_vision: true,
  }
}

/** A local slot: vision comes from a projector, as it does on a real install. */
function localConfig(label: string, vision: boolean): llm.LabelConfig {
  return {
    ...cloudConfig(''),
    label,
    engine: 'llama-server',
    model: 'qwen3-9b.gguf',
    model_path: `/models/${label}.gguf`,
    mmproj: vision ? 'qwen3-9b-mmproj.gguf' : '',
    remote_vision: false,
  }
}

function labels(url: string, localVision = true): llm.LabelsResponse {
  return {
    labels: [
      { label: 'live', config: localConfig('live', localVision), state: 'ready' },
      { label: 'background', config: localConfig('background', localVision), state: 'ready' },
      { label: 'cloud', config: cloudConfig(url), state: url ? 'remote' : 'not_configured' },
    ],
    engines: [],
    voice: [],
    proposal: {},
    recommend: {},
    warnings: [],
  } as unknown as llm.LabelsResponse
}

function ccSettings(model: string): AggregatedSettingsResponse {
  return {
    services: [
      {
        service_name: 'cc',
        success: true,
        error: null,
        latency_ms: 1,
        settings: FEATURES.map((f) => ({
          key: f.key,
          value: model,
          value_type: 'string',
          category: 'models',
          description: null,
          requires_reload: false,
          is_secret: false,
          env_fallback: null,
          from_db: model !== 'auto',
          options: ['auto', 'live', 'background', 'cloud'],
        })),
      },
    ],
    total_services: 1,
    successful_services: 1,
    failed_services: 0,
  }
}

/** One row's option by its visible text: a global role query now matches every row. */
function option(select: HTMLElement, name: string) {
  return Array.from(select.querySelectorAll('option')).find((o) => o.textContent?.trim() === name)
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <FeatureModelsCard />
    </QueryClientProvider>,
  )
}

const NOTICE = /Page contents and screenshots of your browser are sent to Anthropic \(api\.anthropic\.com\) while a task runs/

// The server's computer.model: what getServiceSettings answers and updateSetting writes.
let stored = 'auto'

beforeEach(() => {
  vi.clearAllMocks()
  settings.getServiceSettings.mockImplementation(async () => ccSettings(stored))
  settings.updateSetting.mockImplementation(async (_svc, key, value) => {
    stored = value as string
    return { service_name: 'cc', success: true, key, requires_reload: false, message: null, error: null }
  })
})

describe('FeatureModelsCard', () => {
  it('shows no notice on auto, and the notice naming the provider once cloud is picked', async () => {
    api.getLabels.mockResolvedValue(labels('https://api.anthropic.com'))
    stored = 'auto'
    renderCard()
    const select = await screen.findByLabelText('Computer control')
    await waitFor(() => expect(option(select, 'Cloud (Anthropic (api.anthropic.com))')).toBeEnabled())
    expect(select).toHaveValue('auto')
    expect(screen.queryByText(NOTICE)).not.toBeInTheDocument()

    fireEvent.change(select, { target: { value: 'cloud' } })
    expect(await screen.findByText(NOTICE)).toBeInTheDocument()
    await waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('cc', 'computer.model', 'cloud'))
    expect(select).toHaveValue('cloud')

    fireEvent.change(select, { target: { value: 'live' } })
    await waitFor(() => expect(screen.queryByText(NOTICE)).not.toBeInTheDocument())
    expect(settings.updateSetting).toHaveBeenLastCalledWith('cc', 'computer.model', 'live')
  })

  it('shows the notice for a saved cloud choice', async () => {
    api.getLabels.mockResolvedValue(labels('https://api.anthropic.com/v1'))
    stored = 'cloud'
    renderCard()
    expect(await screen.findByText(NOTICE)).toBeInTheDocument()
  })

  it('offers no cloud until the slot is set up, and warns when cloud is chosen without one', async () => {
    api.getLabels.mockResolvedValue(labels(''))
    stored = 'auto'
    renderCard()
    const select = await screen.findByLabelText('Computer control')
    await waitFor(() => expect(api.getLabels).toHaveBeenCalled())
    expect(option(select, 'Cloud — not set up')).toBeDisabled()
  })

  it('warns when the saved choice is cloud but the slot is not set up', async () => {
    api.getLabels.mockResolvedValue(labels(''))
    stored = 'cloud'
    renderCard()
    expect(await screen.findByText(/The cloud slot isn't set up, so computer control fails until it is/)).toBeInTheDocument()
    expect(screen.queryByText(/are sent to/)).not.toBeInTheDocument()
  })
})

describe('cloudProvider', () => {
  it('names known providers and falls back to the host', () => {
    expect(cloudProvider(cloudConfig('https://api.openai.com/v1'))).toBe('OpenAI (api.openai.com)')
    expect(cloudProvider(cloudConfig('gateway.example:8443/v1'))).toBe('gateway.example')
    expect(cloudProvider(cloudConfig(''))).toBe('')
    expect(cloudProvider({ ...cloudConfig('https://api.openai.com/v1'), engine: 'off' })).toBe('')
    expect(cloudProvider(undefined)).toBe('')
  })
})

describe('the feature table', () => {
  it('lists every feature under a group, with auto naming the slot it resolves to', async () => {
    api.getLabels.mockResolvedValue(labels('https://api.anthropic.com'))
    stored = 'auto'
    renderCard()
    await screen.findByLabelText('Computer control')

    for (const f of FEATURES) {
      expect(screen.getByLabelText(f.title)).toBeInTheDocument()
    }
    for (const title of ['Someone is waiting', 'Runs in the background', 'Needs to see images']) {
      expect(screen.getByRole('heading', { name: title })).toBeInTheDocument()
    }
    // Auto says which slot it means, so a row reads as a value rather than as a word.
    expect(screen.getAllByRole('option', { name: 'Auto — live' }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole('option', { name: 'Auto — background' }).length).toBeGreaterThan(0)
  })

  it('does not offer a slot that cannot see images to a feature that sends them', async () => {
    api.getLabels.mockResolvedValue(labels('https://api.anthropic.com', false))
    stored = 'auto'
    renderCard()
    const vision = await screen.findByLabelText('Computer control')
    await waitFor(() => expect(api.getLabels).toHaveBeenCalled())

    // The local slots have no projector; the cloud slot says it has vision.
    await waitFor(() => expect(option(vision, 'Live — no vision')).toBeDisabled())
    expect(option(vision, 'Background — no vision')).toBeDisabled()
    expect(option(vision, 'Cloud (Anthropic (api.anthropic.com))')).toBeEnabled()

    // A feature that sends no images is unaffected by the same labels.
    const text = screen.getByLabelText('Memory')
    expect(option(text, 'Live')).toBeEnabled()
    expect(option(text, 'Background')).toBeEnabled()
  })

  it('explains a saved choice whose model lost vision, and keeps it selectable', async () => {
    api.getLabels.mockResolvedValue(labels('', false))
    stored = 'background'
    renderCard()
    const select = await screen.findByLabelText('Documents and images')
    expect(
      await screen.findByText(/Documents and images sends images, and the background model can't see them/),
    ).toBeInTheDocument()
    expect(select).toHaveValue('background')
  })
})

describe('slotSeesImages', () => {
  it('reads a local projector and a remote flag', () => {
    expect(slotSeesImages(localConfig('live', true))).toBe(true)
    expect(slotSeesImages(localConfig('live', false))).toBe(false)
    expect(slotSeesImages(cloudConfig('https://api.anthropic.com'))).toBe(true)
    expect(slotSeesImages({ ...cloudConfig('https://api.anthropic.com'), remote_vision: false })).toBe(false)
    expect(slotSeesImages({ ...localConfig('live', true), engine: 'off' })).toBe(false)
    expect(slotSeesImages(undefined)).toBe(false)
  })
})
