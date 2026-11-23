import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PlaceCard, VoiceCard } from './HouseholdVoiceAndPlace'

const api = vi.hoisted(() => ({ getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn(), getPersonaPresets: vi.fn(), getHouseholdTimezone: vi.fn() }))
vi.mock('@/api/household', async (orig) => ({ ...(await orig<typeof import('@/api/household')>()), ...api }))

function renderCard(card: React.ReactNode) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{card}</QueryClientProvider>)
}

describe('household voice and place', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getHouseholdSettings.mockResolvedValue({ 'persona.household_prompt': 'You are Jarvis, helpful and brief.', 'household.location': 'Springfield, IL', 'household.timezone': '' })
    api.getPersonaPresets.mockResolvedValue({
      presets: [
        { id: 'classic', label: 'Classic', text: 'You are Jarvis, helpful and brief.' },
        { id: 'warm', label: 'Warm & folksy', text: 'You are Jarvis, warm and chatty.' },
      ],
      default_preset_id: 'classic', default_text: 'You are Jarvis, helpful and brief.', max_chars: 1200,
    })
    api.getHouseholdTimezone.mockResolvedValue({ timezone: 'America/Chicago', source: 'node', node_timezone: 'America/Chicago' })
    api.putHouseholdSetting.mockResolvedValue(undefined)
  })

  it('an admin loads a starter into the voice and saves it', async () => {
    renderCard(<VoiceCard hh="h1" canEdit />)
    const box = await screen.findByDisplayValue('You are Jarvis, helpful and brief.')
    const starters = screen.getByRole('group', { name: 'Starters' })
    expect(within(starters).getByRole('button', { name: 'Classic (default)' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.click(within(starters).getByRole('button', { name: 'Warm & folksy' }))
    expect(box).toHaveValue('You are Jarvis, warm and chatty.')
    expect(screen.getByText('32/1200')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putHouseholdSetting).toHaveBeenCalledWith('h1', 'persona.household_prompt', 'You are Jarvis, warm and chatty.'))
  })

  it('a member reads the voice but can’t change it', async () => {
    renderCard(<VoiceCard hh="h1" canEdit={false} />)
    expect(await screen.findByText('You are Jarvis, helpful and brief.')).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('group', { name: 'Starters' })).toBeNull()
    expect(screen.getByText('Only a household admin can change this.')).toBeInTheDocument()
  })

  it('saves the town, and the time zone says where Automatic comes from', async () => {
    renderCard(<PlaceCard hh="h1" canEdit />)
    const town = await screen.findByDisplayValue('Springfield, IL')
    const zone = screen.getByLabelText('Time zone')
    expect(await within(zone).findByRole('option', { name: 'Automatic · America / Chicago' })).toBeInTheDocument()
    expect(zone).toHaveValue('')

    fireEvent.change(town, { target: { value: 'Peoria, IL 61602' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putHouseholdSetting).toHaveBeenCalledWith('h1', 'household.location', 'Peoria, IL 61602'))

    fireEvent.change(zone, { target: { value: 'Europe/Paris' } })
    await vi.waitFor(() => expect(api.putHouseholdSetting).toHaveBeenCalledWith('h1', 'household.timezone', 'Europe/Paris'))
    expect(api.getHouseholdTimezone).toHaveBeenCalledTimes(2)
  })
})
