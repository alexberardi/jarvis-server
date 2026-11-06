import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fetchAllHouseholds } from '@/api/admin'
import { updateSetting } from '@/api/settings'
import type { SettingResponse } from '@/types/settings'
import HouseholdValues from './HouseholdValues'

vi.mock('@/api/admin', () => ({ fetchAllHouseholds: vi.fn() }))
vi.mock('@/api/settings', () => ({ updateSetting: vi.fn(), resetHouseholdSetting: vi.fn() }))

const inbound: SettingResponse = {
  key: 'phone.inbound.enabled', value: false, value_type: 'bool', category: 'phone_calls',
  description: null, requires_reload: false, is_secret: false, env_fallback: null,
  from_db: false, options: null, household_scoped: true, household_values: [], households_using_default: 2,
}
const households = [
  { id: 'home', name: 'Home', created_at: '', updated_at: '' },
  { id: 'cabin', name: 'Cabin', created_at: '', updated_at: '' },
]

function show(setting: SettingResponse = inbound) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={client}><HouseholdValues serviceName="cc" setting={setting} /></QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(fetchAllHouseholds).mockResolvedValue(households)
  vi.mocked(updateSetting).mockResolvedValue({ service_name: 'cc', success: true, key: inbound.key, requires_reload: false, message: null, error: null })
})

describe('creating a household setting', () => {
  it('creates the first override only after selecting a household and saving its value', async () => {
    show()
    expect(fetchAllHouseholds).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Set for a household' }))
    const select = await screen.findByRole('combobox', { name: 'Household' })
    expect(select).toHaveValue('')
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
    fireEvent.change(select, { target: { value: 'home' } })
    fireEvent.click(screen.getByRole('switch', { name: inbound.key }))
    expect(updateSetting).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(updateSetting).toHaveBeenCalledWith('cc', inbound.key, true, 'home'))
    expect(await screen.findByRole('button', { name: 'Set for a household' })).toBeVisible()
  })

  it('only offers households without an override and does not write when cancelled', async () => {
    show({ ...inbound, household_values: [{ household_id: 'home', household_name: 'Home', value: true }], households_using_default: 1 })
    fireEvent.click(screen.getByRole('button', { name: 'Set for a household' }))
    const select = await screen.findByRole('combobox', { name: 'Household' })
    expect(screen.queryByRole('option', { name: 'Home' })).not.toBeInTheDocument()
    fireEvent.change(select, { target: { value: 'cabin' } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(updateSetting).not.toHaveBeenCalled()
  })

  it('keeps the selected household and draft on a failed save, with an inline error', async () => {
    vi.mocked(updateSetting).mockRejectedValue(new Error('Could not save this value'))
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Set for a household' }))
    fireEvent.change(await screen.findByRole('combobox', { name: 'Household' }), { target: { value: 'home' } })
    fireEvent.click(screen.getByRole('switch', { name: inbound.key }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save this value')
    expect(screen.getByRole('combobox', { name: 'Household' })).toHaveValue('home')
    expect(screen.getByRole('switch', { name: inbound.key })).toBeChecked()
  })

  it('offers retry when households fail to load', async () => {
    vi.mocked(fetchAllHouseholds).mockRejectedValueOnce(new Error('Could not load households'))
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Set for a household' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load households')
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('combobox', { name: 'Household' })).toBeVisible()
  })

  it('shows when there are no households available', async () => {
    vi.mocked(fetchAllHouseholds).mockResolvedValue([])
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Set for a household' }))
    expect(await screen.findByText('No households available.')).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })
})
