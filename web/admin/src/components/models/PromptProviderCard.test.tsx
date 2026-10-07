import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as authApi from '@/api/auth'
import * as llm from '@/api/llm'
import PromptProviderCard from './PromptProviderCard'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), getSetupState: vi.fn() }))
vi.mock('@/api/llm', async (orig) => ({ ...(await orig<typeof import('@/api/llm')>()), getPromptProvider: vi.fn() }))

const auth = vi.mocked(authApi)
const api = vi.mocked(llm)

const none: llm.PromptProvider = {
  value: '',
  derived: '',
  effective: '',
  source: '',
  valid: false,
  options: ['ChatGPTOpenAI', 'Qwen3_8B_Compressed'],
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
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('warns and offers the pick-list when the assigned live model names none', async () => {
    api.getPromptProvider.mockResolvedValue(none)
    auth.getSetupState.mockResolvedValue(state(true))
    renderCard()
    expect(await screen.findByRole('alert')).toHaveTextContent(/No prompt provider/)
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })

  it('still warns about an unknown override with no model', async () => {
    api.getPromptProvider.mockResolvedValue({ ...none, value: 'Gone', effective: 'Gone', source: 'setting' })
    auth.getSetupState.mockResolvedValue(state(false))
    renderCard()
    expect(await screen.findByRole('alert')).toHaveTextContent(/"Gone" is not a known prompt provider/)
  })
})
