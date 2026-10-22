import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import type { AggregatedSettingsResponse } from '@/types/settings'
import FeatureModelsCard from './FeatureModelsCard'
import { cloudProvider } from './features'

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

function labels(url: string): llm.LabelsResponse {
  return {
    labels: [{ label: 'cloud', config: cloudConfig(url), state: url ? 'remote' : 'not_configured' }],
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
        settings: [
          {
            key: 'computer.model',
            value: model,
            value_type: 'string',
            category: 'computer',
            description: null,
            requires_reload: false,
            is_secret: false,
            env_fallback: null,
            from_db: model !== 'auto',
            options: ['auto', 'live', 'background', 'cloud'],
          },
        ],
      },
    ],
    total_services: 1,
    successful_services: 1,
    failed_services: 0,
  }
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
    await waitFor(() => expect(screen.getByRole('option', { name: 'Cloud (Anthropic (api.anthropic.com))' })).toBeEnabled())
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
    await screen.findByLabelText('Computer control')
    await waitFor(() => expect(api.getLabels).toHaveBeenCalled())
    expect(screen.getByRole('option', { name: 'Cloud (not set up)' })).toBeDisabled()
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
