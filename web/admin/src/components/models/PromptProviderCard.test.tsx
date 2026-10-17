import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as authApi from '@/api/auth'
import * as llm from '@/api/llm'
import * as settingsApi from '@/api/settings'
import PromptProviderCard from './PromptProviderCard'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), getSetupState: vi.fn() }))
vi.mock('@/api/llm', async (orig) => ({ ...(await orig<typeof import('@/api/llm')>()), getPromptProvider: vi.fn() }))
vi.mock('@/api/settings', async (orig) => ({ ...(await orig<typeof import('@/api/settings')>()), updateSetting: vi.fn() }))

const auth = vi.mocked(authApi)
const api = vi.mocked(llm)
const settings = vi.mocked(settingsApi)

const none: llm.PromptProvider = {
  value: '',
  derived: '',
  effective: '',
  source: '',
  valid: false,
  options: ['ChatGPTOpenAI', 'Qwen3_8B_Compressed'],
  tool_calling: { value: 'auto', detected: '', effective: 'text', source: 'default' },
  thinking_switch: { value: 'auto', detected: '', effective: 'none', source: 'default' },
}

const qwen: llm.PromptProvider = {
  ...none,
  derived: 'Qwen3_8B_Compressed',
  effective: 'Qwen3_8B_Compressed',
  source: 'model',
  valid: true,
  tool_calling: { value: 'auto', detected: 'native', effective: 'native', source: 'template' },
  thinking_switch: { value: 'none', detected: 'suffix', effective: 'none', source: 'setting' },
}

function state(modelsConfigured: boolean): authApi.SetupState {
  return {
    needs_superuser: false,
    setup_token_required: false,
    version: 'v',
    superuser: true,
    models_configured: modelsConfigured,
  }
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <PromptProviderCard />
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.clearAllMocks())

describe('PromptProviderCard', () => {
  // A10 F18: the Models step showed a red "voice turns will fail" before anything was installed.
  it('does not warn before a live model is assigned', async () => {
    api.getPromptProvider.mockResolvedValue(none)
    auth.getSetupState.mockResolvedValue(state(false))
    renderCard()
    expect(await screen.findByText(/once one is assigned/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('combobox', { name: 'Prompt provider' })).toBeNull()
  })

  it('warns and offers the pick-list when the assigned live model names none', async () => {
    api.getPromptProvider.mockResolvedValue(none)
    auth.getSetupState.mockResolvedValue(state(true))
    renderCard()
    expect(await screen.findByRole('alert')).toHaveTextContent(/No prompt provider/)
    expect(screen.getByRole('combobox', { name: 'Prompt provider' })).toBeInTheDocument()
  })

  it('still warns about an unknown override with no model', async () => {
    api.getPromptProvider.mockResolvedValue({ ...none, value: 'Gone', effective: 'Gone', source: 'setting' })
    auth.getSetupState.mockResolvedValue(state(false))
    renderCard()
    expect(await screen.findByRole('alert')).toHaveTextContent(/"Gone" is not a known prompt provider/)
  })

  it('shows each mode, where it comes from, and lets the superuser override it', async () => {
    api.getPromptProvider.mockResolvedValue(qwen)
    auth.getSetupState.mockResolvedValue(state(true))
    settings.updateSetting.mockResolvedValue({} as Awaited<ReturnType<typeof settingsApi.updateSetting>>)
    renderCard()

    const tools = await screen.findByLabelText('Tool calling')
    const toolsRow = tools.parentElement as HTMLElement
    expect(within(toolsRow).getByText('native', { selector: 'code' })).toBeInTheDocument()
    expect(within(toolsRow).getByText("from the model's chat template")).toBeInTheDocument()
    expect(within(toolsRow).getByText(/households' prompts may set their own/)).toBeInTheDocument()
    expect(within(toolsRow).queryByText('set by you')).toBeNull()
    expect(within(tools).getByRole('option', { name: 'auto (native)' })).toBeInTheDocument()

    const thinkingRow = screen.getByLabelText('Thinking switch').parentElement as HTMLElement
    expect(within(thinkingRow).getByText('set by you')).toBeInTheDocument()
    expect(within(thinkingRow).getByText('detected: suffix')).toBeInTheDocument()

    fireEvent.change(tools, { target: { value: 'text' } })
    await waitFor(() => expect(settings.updateSetting).toHaveBeenCalledWith('cc', 'llm.tool_calling', 'text'))
  })

  it('says when the model has no detected mode', async () => {
    api.getPromptProvider.mockResolvedValue(none)
    auth.getSetupState.mockResolvedValue(state(true))
    renderCard()
    const row = (await screen.findByLabelText('Tool calling')).parentElement as HTMLElement
    expect(within(row).getByText('not detected')).toBeInTheDocument()
    expect(within(row).getByRole('option', { name: 'auto (not detected)' })).toBeInTheDocument()
  })
})
