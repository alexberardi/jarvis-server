import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import { clearSetupToken, setSetupToken } from '@/auth/setupToken'
import * as authApi from '@/api/auth'
import * as doctorApi from '@/api/doctor'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import * as ttsApi from '@/api/tts'
import type { CatalogEntry, CatalogResponse, LabelStatus, LabelConfig } from '@/api/llm'
import type { SettingResponse } from '@/types/settings'
import { STEP_STORAGE_KEY } from '@/components/wizard/steps'
import SetupWizard from './SetupWizard'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), setup: vi.fn(), getSetupState: vi.fn() }))
vi.mock('@/api/doctor', () => ({ getDoctor: vi.fn() }))
vi.mock('@/api/system', () => ({ getSystemInfo: vi.fn().mockResolvedValue({ listeners: [{ name: 'config', port: 7700, served: true }] }) }))
vi.mock('@/api/settings', () => ({ getAllSettings: vi.fn(), updateSetting: vi.fn() }))
vi.mock('@/api/llm', async (orig) => ({
  ...(await orig<typeof import('@/api/llm')>()),
  getLabels: vi.fn(),
  putLabels: vi.fn(),
  getCatalog: vi.fn(),
  listInstalls: vi.fn(),
  startInstall: vi.fn(),
  getPromptProvider: vi.fn(),
  hfTokenIsSet: vi.fn(),
}))
vi.mock('@/api/tts', () => ({ getVoices: vi.fn(), sampleVoice: vi.fn(), setVoice: vi.fn() }))

const auth = vi.mocked(authApi)
const api = vi.mocked(llm)
const settings = vi.mocked(settingsApi)
const tts = vi.mocked(ttsApi)

function label(name: string): LabelStatus {
  return {
    label: name,
    state: 'not_configured',
    config: { gpu_backend: 'auto', gpu_devices: '', gpu_layers: 999 } as LabelConfig,
  }
}

function setting(key: string, value: boolean): SettingResponse {
  return {
    key,
    value,
    value_type: 'bool',
    category: 'x',
    description: null,
    requires_reload: false,
    is_secret: false,
    env_fallback: null,
    from_db: false,
    options: null,
  }
}

const fit = { verdict: 'fits', needed_mb: 3000, context: 8192, kv_estimated: false }
function entry(id: string, kind: string, display: string, over: Partial<CatalogEntry> = {}): CatalogEntry {
  return { id, display, kind, file: id, size: 1 << 30, sha256: '', fit, installed: false, ...over }
}

const catalog: CatalogResponse = {
  models: [
    entry('qwen3-4b', 'llm', 'Qwen 3 4B'),
    entry('qwen3.5-9b', 'llm', 'Qwen 3.5 9B', { mmproj: 'qwen3.5-9b-mmproj' }),
    entry('qwen3.5-9b-mmproj', 'mmproj', 'projector'),
    entry('whisper-base.en', 'stt', 'Whisper base.en'),
    entry('whisper-small.en', 'stt', 'Whisper small.en'),
    entry('kokoro-multi-lang-v1_0', 'tts', 'Kokoro', { installed: true }),
    entry('eres2net-voxceleb-16k', 'speaker', 'ERes2Net'),
    entry('all-minilm-l6-v2', 'embedding', 'MiniLM'),
  ],
  recommended: {
    live: 'qwen3.5-9b',
    background: 'qwen3.5-9b',
    stt: 'whisper-small.en',
    tts: 'kokoro-multi-lang-v1_0',
    speaker: 'eres2net-voxceleb-16k',
    embeddings: 'all-minilm-l6-v2',
  },
  hardware: { os: 'linux', arch: 'amd64', devices: [], sources: [], flavour: 'cuda', detected_at: '' },
  residents: null,
}

type Job = authApi.SetupJob
const JOB_LABEL: Record<string, Job['label']> = { llm: 'live', stt: 'stt', voice: 'tts', speaker: 'speaker', memory: 'embeddings' }

/** jobs is the server's per-job checklist; every job missing unless given. */
function jobs(states: Record<string, string> = {}, over: Record<string, Partial<Job>> = {}): Job[] {
  return ['llm', 'stt', 'voice', 'speaker', 'memory'].map((j) => ({
    job: j,
    label: JOB_LABEL[j],
    labels: [JOB_LABEL[j]],
    required: ['llm', 'stt', 'voice'].includes(j),
    state: states[j] ?? 'missing',
    label_state: 'not_configured',
    ...over[j],
  }))
}

function signIn() {
  localStorage.setItem('jarvis-admin:access_token', 'a')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true }))
}

const superState: authApi.SetupState = {
  needs_superuser: false,
  setup_token_required: false,
  version: 'v0.1.0',
  superuser: true,
  labels: { live: 'not_configured', stt: 'not_configured', tts: 'ready', speaker: 'ready' },
  live_ready: false,
  models_configured: false,
  hardware: {
    hardware: {
      os: 'linux',
      arch: 'amd64',
      devices: [{ backend: 'cuda', index: 0, id: 'CUDA0', name: 'RTX 3080 Ti', total_mb: 12288, free_mb: 11000 }],
      sources: [],
      flavour: 'cuda',
      detected_at: '',
    },
    proposal: { live: { gpu_backend: 'cuda', gpu_devices: '0' }, background: { gpu_backend: 'cuda', gpu_devices: '0' } },
    flavours: { 'llama-server': ['cpu', 'cuda', 'vulkan'], 'whisper-server': ['cpu', 'cuda'] },
  },
  prompt_provider: { value: '', derived: '', effective: '', source: '', valid: false, options: [] },
  doctor: { status: 'ok', failing: [], ran_at: '' },
  jobs: jobs(),
}

function renderWizard(needsSuperuser = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter initialEntries={['/setup']}>
          <Routes>
            <Route path="/setup" element={<SetupWizard needsSuperuser={needsSuperuser} />} />
            <Route path="/dashboard" element={<p>dashboard page</p>} />
            <Route path="/login" element={<p>login page</p>} />
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  clearSetupToken()
  let signedIn = false
  auth.getSetupState.mockImplementation(async () =>
    signedIn
      ? superState
      : { needs_superuser: true, setup_token_required: true, version: 'v0.1.0', superuser: false, setup_token_file: '/h/setup-token' },
  )
  auth.setup.mockImplementation(async () => {
    signedIn = true
    return {
      access_token: 'acc',
      refresh_token: 'ref',
      token_type: 'bearer',
      user: { id: 1, email: 'op@example.com', is_superuser: true },
    }
  })
  vi.mocked(doctorApi.getDoctor).mockResolvedValue({
    status: 'warn',
    ran_at: '',
    checks: [
      { name: 'listener config :7700', status: 'ok', detail: 'answers' },
      { name: 'firewall 10.0.0.0/24', status: 'warn', detail: 'blocked', fix: 'sudo ufw allow from 10.0.0.0/24' },
    ],
  })
  api.getLabels.mockResolvedValue({
    labels: ['live', 'background', 'embeddings', 'stt'].map(label),
    voice: [],
    engines: [],
    proposal: { stt: { gpu_backend: 'cuda', gpu_devices: '0' } },
    recommend: {},
    warnings: null,
  })
  api.getCatalog.mockResolvedValue(catalog)
  api.listInstalls.mockResolvedValue([])
  let nextID = 1
  api.startInstall.mockImplementation(async (req) => ({
    install: {
      id: nextID++,
      model_id: req.catalog_id ?? '',
      assign: req.assign ?? [],
      state: 'queued',
      phase: '',
      bytes_total: 0,
      bytes_done: 0,
      created_at: '',
      updated_at: '',
    },
    existing: false,
  }))
  api.getPromptProvider.mockResolvedValue(superState.prompt_provider!)
  api.hfTokenIsSet.mockResolvedValue(false)
  tts.getVoices.mockResolvedValue({ voices: ['af_heart', 'bm_george', 'bm_lewis'], current: 'bm_george', default: 'bm_george' })
  tts.setVoice.mockResolvedValue()
  settings.getAllSettings.mockResolvedValue({
    services: [
      {
        service_name: 'cc',
        success: true,
        error: null,
        latency_ms: 1,
        settings: [
          setting('web_search.enabled', false),
          setting('web_scraping.allow_external', false),
          setting('memory.enabled', true),
          setting('memory.extraction_enabled', true),
          setting('ambient_context.enabled', false),
          setting('pantry.enabled', false),
        ],
      },
      { service_name: 'admin', success: true, error: null, latency_ms: 1, settings: [setting('updates.enabled', false)] },
    ],
    total_services: 2,
    successful_services: 2,
    failed_services: 0,
  })
  settings.updateSetting.mockResolvedValue({
    service_name: 'cc',
    success: true,
    key: 'web_search.enabled',
    requires_reload: false,
    message: null,
    error: null,
  })
})

describe('setup wizard (AD3, AD3a, AD3b)', () => {
  it('walks Check → Account → Hardware → the five model jobs → Privacy → Done', async () => {
    setSetupToken('tok')
    renderWizard()

    // Check: doctor runs without a token; problems show their fix but don't block.
    expect(await screen.findByText('firewall 10.0.0.0/24')).toBeInTheDocument()
    expect(screen.getByText('sudo ufw allow from 10.0.0.0/24')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    // Account.
    fireEvent.change(await screen.findByLabelText('Display Name'), { target: { value: 'Op' } })
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'op@example.com' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'password123' } })
    fireEvent.change(screen.getByLabelText('Confirm Password'), { target: { value: 'password123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))
    await waitFor(() => expect(auth.setup).toHaveBeenCalledWith('op@example.com', 'password123', 'Op', 'tok'))

    // Hardware: pre-filled to detection; one click accepts without writing anything.
    expect(await screen.findByText('RTX 3080 Ti')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /NVIDIA CUDA/ })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'On the GPU (fast)' })).toBeChecked()
    expect(sessionStorage.getItem(STEP_STORAGE_KEY)).toBe('hardware')
    fireEvent.click(screen.getByRole('button', { name: 'Looks good' }))
    expect(api.putLabels).not.toHaveBeenCalled()

    // Language model: pre-filled with the hardware-fit recommendation, its projector included.
    expect(await screen.findByRole('heading', { name: /Language model/ })).toBeInTheDocument()
    expect(await screen.findByRole('radio', { name: 'Qwen 3.5 9B' })).toBeChecked()
    expect(screen.getByText(/Includes its vision projector/)).toBeInTheDocument()
    // Another choice; confirming installs it (for background too) and moves on at once.
    fireEvent.click(screen.getByRole('radio', { name: 'Qwen 3 4B' }))
    fireEvent.click(screen.getByRole('button', { name: 'Install and continue' }))
    await waitFor(() => expect(api.startInstall).toHaveBeenCalledWith({ catalog_id: 'qwen3-4b', assign: ['live', 'background'] }))

    // Speech-to-text: required but skippable; says where it runs.
    expect(await screen.findByRole('heading', { name: /Speech-to-text/ })).toBeInTheDocument()
    expect(await screen.findByRole('radio', { name: 'Whisper small.en' })).toBeChecked()
    expect(screen.getByText(/Runs on the GPU \(cuda\), as set in the Hardware step/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))

    // Voice, Voice recognition, Memory & search: skipped.
    expect(await screen.findByRole('heading', { name: /^Voice Required/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(await screen.findByRole('heading', { name: /Voice recognition/ })).toBeInTheDocument()
    expect(screen.getByText(/enrols their voice later in the Jarvis mobile app/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Skip' }))
    expect(await screen.findByRole('heading', { name: /Memory & search/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Skip' }))
    expect(api.startInstall).toHaveBeenCalledTimes(1)
    // Each step is recorded for other tabs and browsers.
    expect(settings.updateSetting).toHaveBeenCalledWith('admin', 'setup.step', 'memory')

    // Privacy: off-box features start off; turning one on writes just that key.
    const search = await screen.findByRole('switch', { name: /Web search/ })
    expect(search).not.toBeChecked()
    expect(screen.getByRole('switch', { name: /Pantry package store/ })).not.toBeChecked()
    expect(screen.getByRole('switch', { name: /Memories/ })).toBeChecked()
    expect(screen.getByRole('switch', { name: /Speaker recognition/ })).toBeDisabled() // stt not listed here
    fireEvent.click(search)
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('cc', 'web_search.enabled', true))
    expect(settings.updateSetting).not.toHaveBeenCalledWith('cc', 'pantry.enabled', expect.anything())

    // Done: recorded on the server, so no tab or browser resumes the wizard again (A10 F9).
    expect(await screen.findByText('Jarvis is set up')).toBeInTheDocument()
    expect(settings.updateSetting).toHaveBeenCalledWith('admin', 'setup.completed', true)
    expect(
      screen.getByText(/Voice requests won't work until Language model, Speech-to-text and Voice are installed/),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Go to the dashboard/ }))
    expect(await screen.findByText('dashboard page')).toBeInTheDocument()
    expect(sessionStorage.getItem(STEP_STORAGE_KEY)).toBeNull()
  })

  // A10 rehearsal: a refused setup (a .local email → 422) blanked the whole form and showed
  // nothing, because the wizard unmounted while the auth call was in flight.
  it('keeps the account form and shows why setup was refused', async () => {
    setSetupToken('tok')
    auth.setup.mockRejectedValueOnce(
      new AxiosError('Request failed with status code 422', 'ERR_BAD_REQUEST', undefined, undefined, {
        status: 422,
        statusText: 'Unprocessable Entity',
        headers: {},
        config: {} as InternalAxiosRequestConfig,
        data: {
          detail: [
            {
              type: 'value_error',
              loc: ['body', 'email'],
              msg: 'value is not a valid email address: The part after the @-sign is a special-use or reserved name that cannot be used with email.',
            },
          ],
        },
      }),
    )
    renderWizard()
    fireEvent.click(await screen.findByRole('button', { name: 'Continue' }))
    fireEvent.change(await screen.findByLabelText('Display Name'), { target: { value: 'Op' } })
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'op@jarvis.local' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'password123' } })
    fireEvent.change(screen.getByLabelText('Confirm Password'), { target: { value: 'password123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/special-use or reserved name/)
    expect(screen.getByLabelText('Email')).toHaveValue('op@jarvis.local')
    expect(screen.getByLabelText('Display Name')).toHaveValue('Op')

    // Corrected, it goes through on the same form.
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'op@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))
    await waitFor(() => expect(auth.setup).toHaveBeenLastCalledWith('op@example.com', 'password123', 'Op', 'tok'))
    expect(await screen.findByText('RTX 3080 Ti')).toBeInTheDocument()
  })

  it('saves changed hardware choices through the labels PUT', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'hardware')
    auth.getSetupState.mockResolvedValue(superState)
    api.putLabels.mockResolvedValue({ labels: [], voice: [], engines: [], proposal: {}, recommend: {}, warnings: null })
    renderWizard()

    fireEvent.click(await screen.findByRole('radio', { name: 'On the CPU' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ stt: { gpu_backend: 'cpu' } }))
    expect(await screen.findByRole('heading', { name: /Language model/ })).toBeInTheDocument()
  })

  // AD3b browser run: a CUDA box set to CPU in the Hardware step was still offered Qwen 3.5 9B.
  it('recommends the CPU-sized models after choosing the CPU in the Hardware step', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'hardware')
    auth.getSetupState.mockResolvedValue(superState)
    // The server judges the catalog for the labels' builds: once they are on the CPU, the
    // small model is recommended and every verdict is "runs on the CPU".
    const cpuFit = { verdict: 'cpu', needed_mb: 3200, context: 16384, kv_estimated: false, ram_mb: 32768 }
    const onCPU: CatalogResponse = {
      ...catalog,
      models: catalog.models.map((m) => ({ ...m, fit: m.kind === 'llm' || m.kind === 'stt' ? cpuFit : m.fit })),
      recommended: { ...catalog.recommended, live: 'qwen3-4b', background: 'qwen3-4b', stt: 'whisper-small.en' },
    }
    api.getCatalog.mockImplementation(async () => (api.putLabels.mock.calls.length > 0 ? onCPU : catalog))
    const cpuLabel = (name: string): LabelStatus => ({ ...label(name), config: { ...label(name).config, gpu_backend: 'cpu' } })
    const labelsResponse = () => ({
      labels: ['live', 'background', 'embeddings', 'stt'].map(api.putLabels.mock.calls.length > 0 ? cpuLabel : label),
      voice: [],
      engines: [],
      proposal: { stt: { gpu_backend: 'cuda', gpu_devices: '0' } },
      recommend: {},
      warnings: null,
    })
    api.getLabels.mockImplementation(async () => labelsResponse())
    api.putLabels.mockImplementation(async () => labelsResponse())
    renderWizard()

    fireEvent.click(await screen.findByRole('radio', { name: 'CPU only' }))
    fireEvent.click(screen.getByRole('radio', { name: 'On the CPU' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() =>
      expect(api.putLabels).toHaveBeenCalledWith({
        live: { gpu_backend: 'cpu' },
        background: { gpu_backend: 'cpu' },
        embeddings: { gpu_backend: 'cpu' },
        stt: { gpu_backend: 'cpu' },
      }),
    )

    expect(await screen.findByRole('heading', { name: /Language model/ })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('radio', { name: 'Qwen 3 4B' })).toBeChecked())
    const q4 = screen.getByRole('radio', { name: 'Qwen 3 4B' }).closest('label')!
    expect(within(q4).getByText('Recommended')).toBeInTheDocument()
    expect(within(q4).getByText('Runs on CPU')).toBeInTheDocument()
    expect(within(q4).getByText(/Runs on the CPU \(needs about 3\.1 GB of 32\.0 GB of RAM\)/)).toBeInTheDocument()
    expect(await screen.findByText(/Runs on the CPU, as set in the Hardware step, so the recommendation is a small model/)).toBeInTheDocument()
    const q9 = screen.getByRole('radio', { name: 'Qwen 3.5 9B' }).closest('label')!
    expect(within(q9).queryByText('Recommended')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Install and continue' }))
    await waitFor(() => expect(api.startInstall).toHaveBeenCalledWith({ catalog_id: 'qwen3-4b', assign: ['live', 'background'] }))
    expect(await screen.findByRole('heading', { name: /Speech-to-text/ })).toBeInTheDocument()
    expect(await screen.findByRole('radio', { name: 'Whisper small.en' })).toBeChecked()
    expect(await screen.findByText(/Runs on the CPU, as set in the Hardware step; the recommendation is sized for that/)).toBeInTheDocument()
  })

  it('"Install everything recommended" confirms all five jobs and goes on to Privacy', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'llm')
    auth.getSetupState.mockResolvedValue({ ...superState, jobs: jobs({ voice: 'ready' }) })
    renderWizard(false)

    fireEvent.click(await screen.findByRole('button', { name: /Install everything recommended/ }))
    await waitFor(() => expect(api.startInstall).toHaveBeenCalledTimes(4))
    // One install per model: the live model serves background too; Voice is ready already.
    expect(api.startInstall.mock.calls.map((c) => c[0])).toEqual([
      { catalog_id: 'qwen3.5-9b', assign: ['live', 'background'], with_mmproj: true },
      { catalog_id: 'whisper-small.en', assign: ['stt'] },
      { catalog_id: 'eres2net-voxceleb-16k', assign: ['speaker'] },
      { catalog_id: 'all-minilm-l6-v2', assign: ['embeddings'] },
    ])
    expect(await screen.findByRole('switch', { name: /Web search/ })).toBeInTheDocument()
    expect(settings.updateSetting).toHaveBeenCalledWith('admin', 'setup.step', 'privacy')
  })

  it('shows a confirmed job downloading in the background and moves on without installing again', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'llm')
    const install = { id: 7, model_id: 'qwen3-4b', state: 'running', phase: 'model', bytes_done: 40, bytes_total: 100 }
    auth.getSetupState.mockResolvedValue({ ...superState, jobs: jobs({ llm: 'downloading' }, { llm: { install } }) })
    renderWizard(false)

    expect(await screen.findByText(/Downloading 40% · qwen3-4b/)).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Language model download' })).toHaveAttribute('aria-valuenow', '40')
    // Pre-filled to what is downloading; nothing to skip, just continue.
    expect(await screen.findByRole('radio', { name: 'Qwen 3 4B' })).toBeChecked()
    expect(screen.queryByRole('button', { name: 'Skip for now' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByRole('heading', { name: /Speech-to-text/ })).toBeInTheDocument()
    expect(api.startInstall).not.toHaveBeenCalled()
  })

  it('plays a voice sample and saves the chosen voice', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'voice')
    auth.getSetupState.mockResolvedValue({ ...superState, jobs: jobs({ voice: 'ready' }) })
    api.getLabels.mockResolvedValue({
      labels: ['live', 'background', 'embeddings', 'stt'].map(label),
      voice: [{ label: 'tts', id: 'kokoro-multi-lang-v1_0', kind: 'tts', path: '/m/kokoro' }],
      engines: [],
      proposal: {},
      recommend: {},
      warnings: null,
    })
    tts.sampleVoice.mockResolvedValue(new Blob(['RIFF'], { type: 'audio/wav' }))
    const play = vi.spyOn(window.HTMLMediaElement.prototype, 'play').mockResolvedValue()
    URL.createObjectURL = vi.fn(() => 'blob:sample')
    URL.revokeObjectURL = vi.fn()
    renderWizard(false)

    const select = await screen.findByLabelText('Voice')
    await waitFor(() => expect(select).toHaveValue('bm_george'))
    fireEvent.change(select, { target: { value: 'af_heart' } })
    fireEvent.click(screen.getByRole('button', { name: /Play sample/ }))
    await waitFor(() => expect(tts.sampleVoice).toHaveBeenCalledWith('af_heart'))
    await waitFor(() => expect(play).toHaveBeenCalled())

    // Kokoro runs already, so confirming only saves the voice.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(tts.setVoice).toHaveBeenCalledWith('af_heart'))
    expect(await screen.findByRole('heading', { name: /Voice recognition/ })).toBeInTheDocument()
    expect(api.startInstall).not.toHaveBeenCalled()
    play.mockRestore()
  })

  it('waits for the voice model before offering a sample', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'voice')
    auth.getSetupState.mockResolvedValue(superState)
    renderWizard(false)
    expect(await screen.findByRole('button', { name: /Play sample/ })).toBeDisabled()
    expect(screen.getByText(/sample plays once the voice model has downloaded/)).toBeInTheDocument()
  })

  it('Done lists every job, flags the required ones still missing, and still finishes', async () => {
    signIn()
    sessionStorage.setItem(STEP_STORAGE_KEY, 'done')
    const failed = { id: 3, model_id: 'whisper-small.en', state: 'failed', phase: 'model', bytes_done: 0, bytes_total: 9, error: 'sha256 mismatch' }
    const running = { id: 1, model_id: 'qwen3-4b', state: 'running', phase: 'model', bytes_done: 1, bytes_total: 4 }
    auth.getSetupState.mockResolvedValue({
      ...superState,
      models_configured: true,
      jobs: jobs(
        { llm: 'downloading', stt: 'failed', voice: 'missing', speaker: 'ready', memory: 'missing' },
        { llm: { install: running }, stt: { install: failed } },
      ),
    })
    renderWizard(false)

    expect(await screen.findByText(/Voice requests won't work until Speech-to-text and Voice are installed/)).toBeInTheDocument()
    const row = (id: string) => screen.getByTestId(`job-${id}`)
    expect(row('llm')).toHaveTextContent('Downloading 25%')
    expect(row('stt')).toHaveTextContent('Failed')
    expect(row('stt')).toHaveTextContent('whisper-small.en: sha256 mismatch')
    expect(row('voice')).toHaveTextContent('Skipped')
    expect(row('voice')).toHaveTextContent("Voice requests can't work without it")
    expect(row('speaker')).toHaveTextContent('Ready')
    expect(row('memory')).toHaveTextContent('Skipped')
    expect(row('memory')).not.toHaveTextContent("can't work")
    fireEvent.click(screen.getByRole('button', { name: /Go to the dashboard/ }))
    expect(await screen.findByText('dashboard page')).toBeInTheDocument()
  })

  // A10 F9: closing the tab mid-download and signing in again (or another browser) used to land
  // on the dashboard, skipping Privacy and Done; the server now says where setup stands.
  it('resumes in a new tab from the server state', async () => {
    signIn()
    auth.getSetupState.mockResolvedValue({ ...superState, models_configured: true, setup_completed: false, setup_step: 'voice' })
    renderWizard(false)
    expect(await screen.findByRole('heading', { name: /^Voice Required/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Privacy/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: /Speech-to-text/ })).toBeEnabled()
  })

  it('forwards to the dashboard once setup was finished', async () => {
    signIn()
    auth.getSetupState.mockResolvedValue({ ...superState, setup_completed: true, setup_step: '' })
    renderWizard(false)
    expect(await screen.findByText('dashboard page')).toBeInTheDocument()
  })
})
