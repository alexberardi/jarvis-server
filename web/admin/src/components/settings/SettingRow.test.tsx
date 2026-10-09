import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as api from '@/api/settings'
import type { SettingResponse } from '@/types/settings'
import SettingRow from './SettingRow'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('@/api/settings', () => ({
  getAllSettings: vi.fn(),
  updateSetting: vi.fn(),
  resetHouseholdSetting: vi.fn(),
}))

const m = vi.mocked(api)

function setting(extra: Partial<SettingResponse> = {}): SettingResponse {
  return {
    key: 'pantry.enabled',
    value: false,
    value_type: 'bool',
    category: 'pantry',
    description: 'May the household use the Pantry package store',
    requires_reload: false,
    is_secret: false,
    env_fallback: null,
    from_db: false,
    options: null,
    ...extra,
  }
}

function renderRow(s: SettingResponse) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SettingRow setting={s} serviceName="cc" />
    </QueryClientProvider>,
  )
}

const ok = { service_name: 'cc', success: true, key: 'pantry.enabled', requires_reload: false, message: null, error: null }

beforeEach(() => {
  vi.clearAllMocks()
  m.updateSetting.mockResolvedValue(ok)
  m.resetHouseholdSetting.mockResolvedValue(ok)
})

describe('SettingRow household values', () => {
  it('shows the default and each household with its own value', () => {
    renderRow(
      setting({
        household_scoped: true,
        household_values: [
          { household_id: 'hh-home', household_name: 'Home', value: true },
          { household_id: 'hh-cabin', household_name: 'Cabin', value: false },
        ],
        households_using_default: 1,
      }),
    )
    expect(screen.getByText('Default for all households')).toBeInTheDocument()
    const home = screen.getByTestId('household-value-hh-home')
    expect(within(home).getByText('Home')).toBeInTheDocument()
    expect(within(home).getByText('true')).toBeInTheDocument()
    expect(within(screen.getByTestId('household-value-hh-cabin')).getByText('false')).toBeInTheDocument()
    expect(screen.getByText('1 household uses the default')).toBeInTheDocument()
    expect(screen.queryByText('Households can change this in the app')).toBeNull()
  })

  it('"Use default" removes that household\'s value', async () => {
    renderRow(
      setting({
        household_scoped: true,
        household_values: [{ household_id: 'hh-home', household_name: 'Home', value: true }],
        households_using_default: 0,
      }),
    )
    expect(screen.queryByText(/use the default/)).toBeNull() // nobody else is on the default
    fireEvent.click(within(screen.getByTestId('household-value-hh-home')).getByRole('button', { name: 'Use default' }))
    await waitFor(() => expect(m.resetHouseholdSetting).toHaveBeenCalledWith('cc', 'pantry.enabled', 'hh-home'))
    expect(m.updateSetting).not.toHaveBeenCalled()
  })

  it('edits a household value with that household id, and the default without one', async () => {
    renderRow(
      setting({
        household_scoped: true,
        household_values: [{ household_id: 'hh-home', household_name: 'Home', value: true }],
        households_using_default: 2,
      }),
    )
    const home = screen.getByTestId('household-value-hh-home')
    fireEvent.click(within(home).getByRole('button', { name: 'Edit Home' }))
    fireEvent.click(within(home).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(m.updateSetting).toHaveBeenCalledWith('cc', 'pantry.enabled', true, 'hh-home'))

    fireEvent.click(screen.getByRole('button', { name: 'Edit the default' }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])
    await waitFor(() => expect(m.updateSetting).toHaveBeenCalledWith('cc', 'pantry.enabled', false, undefined))
  })

  it('notes that households can change it when none has', () => {
    renderRow(setting({ household_scoped: true, household_values: [], households_using_default: 3 }))
    expect(screen.getByText('Households can change this in the app')).toBeInTheDocument()
    expect(screen.getByText('Default for all households')).toBeInTheDocument()
  })

  it('masks a secret household value', () => {
    renderRow(
      setting({
        key: 'phone.twilio_auth_token',
        value_type: 'string',
        value: '',
        is_secret: true,
        household_scoped: true,
        household_values: [{ household_id: 'hh-home', household_name: 'Home', value: '********' }],
        households_using_default: 0,
      }),
    )
    expect(within(screen.getByTestId('household-value-hh-home')).getByText('set (hidden)')).toBeInTheDocument()
  })

  it('leaves a system-only setting as it was', () => {
    renderRow(setting({ key: 'llm.prompt_provider', value_type: 'string', value: 'x' }))
    expect(screen.queryByText('Default for all households')).toBeNull()
    expect(screen.queryByText('Households can change this in the app')).toBeNull()
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument()
  })
})
