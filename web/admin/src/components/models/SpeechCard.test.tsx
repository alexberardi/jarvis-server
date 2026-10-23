import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import * as speech from '@/api/speech'
import * as tts from '@/api/tts'
import type { AggregatedSettingsResponse, SettingResponse } from '@/types/settings'
import SpeechCard from './SpeechCard'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/llm', async (orig) => ({ ...(await orig<typeof import('@/api/llm')>()), getLabels: vi.fn() }))
vi.mock('@/api/settings', () => ({ getServiceSettings: vi.fn(), updateSetting: vi.fn() }))
vi.mock('@/api/speech', async (orig) => ({
  ...(await orig<typeof import('@/api/speech')>()),
  getSpeechPresets: vi.fn(),
}))
vi.mock('@/api/tts', async (orig) => ({ ...(await orig<typeof import('@/api/tts')>()), getVoices: vi.fn(), sampleVoice: vi.fn() }))

const api = vi.mocked(llm)
const settings = vi.mocked(settingsApi)
const speechApi = vi.mocked(speech)
const ttsApi = vi.mocked(tts)

function setting(key: string, value: unknown, is_secret = false): SettingResponse {
  return {
    key,
    value,
    value_type: typeof value === 'number' ? 'int' : 'string',
    category: 'tts',
    description: null,
    requires_reload: false,
    is_secret,
    env_fallback: null,
    from_db: true,
    options: null,
  }
}

function ttsSettings(over: Record<string, unknown> = {}): AggregatedSettingsResponse {
  const values: Record<string, unknown> = {
    'tts.provider': 'kokoro',
    'tts.remote_url': '',
    'tts.remote_api': 'openai',
    'tts.remote_model': '',
    'tts.remote_api_key': '********',
    'tts.remote_voice': '',
    'tts.remote_timeout_ms': 8000,
    'tts.fallback': 'off',
    'tts.kokoro_voice': 'bm_george',
    ...over,
  }
  return {
    services: [
      {
        service_name: 'tts',
        success: true,
        settings: Object.entries(values).map(([k, v]) => setting(k, v, k === 'tts.remote_api_key')),
        error: null,
        latency_ms: 1,
      },
    ],
    total_services: 1,
    successful_services: 1,
    failed_services: 0,
  }
}

const kokoroVoices: tts.VoicesResponse = {
  provider: 'kokoro',
  voices: ['af_heart', 'bm_george'],
  current: 'bm_george',
  default: 'af_heart',
  setting: 'tts/tts.kokoro_voice',
  free_text: false,
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SpeechCard />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getLabels.mockResolvedValue({ labels: [], voice: [], engines: [], proposal: {}, recommend: {}, warnings: null })
  speechApi.getSpeechPresets.mockResolvedValue({
    stt: [],
    tts: [
      { id: 'openai', name: 'OpenAI', url: 'https://api.openai.com/v1', api: 'openai', tts_model: 'gpt-4o-mini-tts', tts_voice: 'marin' },
      { id: 'other', name: 'Other', url: '', api: 'openai', note: 'Any OpenAI-compatible /audio/speech.' },
    ],
  })
  ttsApi.getVoices.mockResolvedValue(kokoroVoices)
  settings.getServiceSettings.mockResolvedValue(ttsSettings())
  settings.updateSetting.mockResolvedValue({ success: true } as never)
})

describe('SpeechCard (docs/llm/07-remote-speech.md §4)', () => {
  it('shows Kokoro with its voices and no remote fields until remote is picked', async () => {
    renderCard()
    const card = await screen.findByTestId('speech-card')
    expect(await within(card).findByLabelText('Runs')).toHaveValue('kokoro')
    expect(within(card).queryByLabelText('Base URL')).toBeNull()
    expect(await within(card).findByLabelText('Voice')).toHaveValue('bm_george')
  })

  it('fills URL, model and voice from a preset and saves each changed setting, the key write-only', async () => {
    renderCard()
    const card = await screen.findByTestId('speech-card')
    fireEvent.change(await within(card).findByLabelText('Runs'), { target: { value: 'remote' } })
    expect(within(card).getByText(/leaves this machine as text/)).toBeInTheDocument()

    await within(card).findByRole('option', { name: 'OpenAI' })
    fireEvent.change(within(card).getByLabelText('Provider'), { target: { value: 'openai' } })
    expect(within(card).getByLabelText('Base URL')).toHaveValue('https://api.openai.com/v1')
    expect(within(card).getByLabelText('Model name')).toHaveValue('gpt-4o-mini-tts')

    // The stored key never shows; typing one writes it.
    const key = within(card).getByLabelText('API key')
    expect(key).toHaveValue('')
    fireEvent.change(key, { target: { value: 'sk-test' } })

    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.provider', 'remote'))
    expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.remote_url', 'https://api.openai.com/v1')
    expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.remote_model', 'gpt-4o-mini-tts')
    expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.remote_api_key', 'sk-test')
    // The voice goes to the setting the server names (still Kokoro's until the save lands).
    expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.kokoro_voice', 'marin')
    // Unchanged settings are not rewritten.
    expect(settings.updateSetting).not.toHaveBeenCalledWith('tts', 'tts.remote_timeout_ms', expect.anything())
  })

  it('leaves the stored key alone when the field stays empty', async () => {
    renderCard()
    const card = await screen.findByTestId('speech-card')
    fireEvent.change(await within(card).findByLabelText('Runs'), { target: { value: 'remote' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.provider', 'remote'))
    expect(settings.updateSetting).not.toHaveBeenCalledWith('tts', 'tts.remote_api_key', expect.anything())
  })

  it('offers free text when the provider lists no voices', async () => {
    ttsApi.getVoices.mockResolvedValue({
      provider: 'remote',
      voices: [],
      current: 'vibes',
      default: '',
      setting: 'tts/tts.remote_voice',
      free_text: true,
    })
    settings.getServiceSettings.mockResolvedValue(ttsSettings({ 'tts.provider': 'remote' }))
    renderCard()
    const card = await screen.findByTestId('speech-card')
    const voice = await within(card).findByLabelText('Voice')
    expect(voice.tagName).toBe('INPUT')
    fireEvent.change(voice, { target: { value: 'george-clone' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('tts', 'tts.remote_voice', 'george-clone'))
  })

  it('shows the fallback warning from the tts voice label with a link to the catalog', async () => {
    settings.getServiceSettings.mockResolvedValue(ttsSettings({ 'tts.provider': 'remote' }))
    api.getLabels.mockResolvedValue({
      labels: [],
      voice: [
        {
          label: 'tts',
          id: '',
          kind: 'tts',
          path: '',
          fallback: { mode: 'cpu', fallbacks: 2, last_remote_error: 'remote tts: 500', warning: 'no Kokoro model for the local fallback' },
        },
      ],
      engines: [],
      proposal: {},
      recommend: {},
      warnings: null,
    })
    renderCard()
    const card = await screen.findByTestId('speech-card')
    expect(await within(card).findByText(/no Kokoro model for the local fallback/)).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: 'Install one from the catalog' })).toHaveAttribute(
      'href',
      '/admin/models?job=tts&tab=catalog',
    )
    expect(within(card).getByText(/Kokoro answered 2 requests/)).toBeInTheDocument()
  })

  it('plays a test sentence and says which engine rendered it', async () => {
    ttsApi.sampleVoice.mockResolvedValue({ blob: new Blob(['RIFF'], { type: 'audio/wav' }), engine: 'remote' })
    const play = vi.spyOn(window.HTMLMediaElement.prototype, 'play').mockResolvedValue()
    URL.createObjectURL = vi.fn(() => 'blob:sample')
    URL.revokeObjectURL = vi.fn()
    renderCard()
    const card = await screen.findByTestId('speech-card')
    const button = await within(card).findByRole('button', { name: 'Play test sentence' })
    fireEvent.click(button)
    const result = await within(card).findByRole('status')
    expect(result).toHaveTextContent(/Rendered in .*, by the remote provider\./)
    expect(ttsApi.sampleVoice).toHaveBeenCalledWith('bm_george')
    expect(play).toHaveBeenCalled()

    // A dirty form disables the test: the sample uses what is saved.
    fireEvent.change(within(card).getByLabelText('Runs'), { target: { value: 'remote' } })
    expect(within(card).getByRole('button', { name: 'Play test sentence' })).toBeDisabled()
  })
})
