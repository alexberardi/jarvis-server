import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import { clearSetupToken, setSetupToken } from '@/auth/setupToken'
import * as authApi from '@/api/auth'
import * as doctorApi from '@/api/doctor'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import type { LabelStatus, LabelConfig } from '@/api/llm'
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
  getPromptProvider: vi.fn(),
  hfTokenIsSet: vi.fn(),
}))

const auth = vi.mocked(authApi)
const api = vi.mocked(llm)
const settings = vi.mocked(settingsApi)

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
}

function renderWizard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter initialEntries={['/setup']}>
          <Routes>
            <Route path="/setup" element={<SetupWizard needsSuperuser />} />
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
    proposal: {},
    recommend: {},
    warnings: null,
  })
  api.getCatalog.mockResolvedValue({ models: [], recommended: {}, hardware: superState.hardware!.hardware, residents: null })
  api.listInstalls.mockResolvedValue([])
  api.getPromptProvider.mockResolvedValue(superState.prompt_provider!)
  api.hfTokenIsSet.mockResolvedValue(false)
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

describe('setup wizard (AD3, AD3a)', () => {
  it('walks Check → Account → Hardware → Models → Privacy → Done', async () => {
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

    // Models: skippable while nothing is assigned.
    expect(await screen.findByRole('button', { name: 'Continue' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))

    // Privacy: off-box features start off; turning one on writes just that key.
    const search = await screen.findByRole('switch', { name: /Web search/ })
    expect(search).not.toBeChecked()
    expect(screen.getByRole('switch', { name: /Memories/ })).toBeChecked()
    expect(screen.getByRole('switch', { name: /Speaker recognition/ })).toBeDisabled() // stt not listed here
    fireEvent.click(search)
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(settings.updateSetting).toHaveBeenCalledTimes(1))
    expect(settings.updateSetting).toHaveBeenCalledWith('cc', 'web_search.enabled', true)

    // Done.
    expect(await screen.findByText('Jarvis is set up')).toBeInTheDocument()
    expect(screen.getByText(/No language model is assigned yet/)).toBeInTheDocument()
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
    localStorage.setItem('jarvis-admin:access_token', 'a')
    localStorage.setItem('jarvis-admin:refresh_token', 'r')
    localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true }))
    sessionStorage.setItem(STEP_STORAGE_KEY, 'hardware')
    auth.getSetupState.mockResolvedValue(superState)
    api.putLabels.mockResolvedValue({ labels: [], voice: [], engines: [], proposal: {}, recommend: {}, warnings: null })
    renderWizard()

    fireEvent.click(await screen.findByRole('radio', { name: 'On the CPU' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }))
    await waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ stt: { gpu_backend: 'cpu' } }))
    expect(await screen.findByText(/Install recommended/)).toBeInTheDocument()
  })
})
