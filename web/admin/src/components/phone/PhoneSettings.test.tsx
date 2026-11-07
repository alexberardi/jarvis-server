import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { getServiceSettings, updateSetting } from '@/api/settings'
import type { SettingResponse } from '@/types/settings'
import { whoIsOn } from '@/lib/settings'
import PhoneSettings from './PhoneSettings'

vi.mock('@/api/settings', () => ({ getServiceSettings: vi.fn(), updateSetting: vi.fn(), resetHouseholdSetting: vi.fn() }))
vi.mock('@/api/admin', () => ({ fetchAllHouseholds: vi.fn().mockResolvedValue([]) }))

function s(key: string, value: unknown, over: Partial<SettingResponse> = {}): SettingResponse {
  return {
    key, value, value_type: typeof value === 'boolean' ? 'bool' : typeof value === 'number' ? 'int' : 'string',
    category: 'phone_calls', description: `server: ${key}`, requires_reload: false, is_secret: false,
    env_fallback: null, from_db: false, options: null, ...over,
  }
}

const hh = (values: { household_id: string; household_name: string; value: unknown }[], usingDefault: number) => ({
  household_scoped: true, household_values: values, households_using_default: usingDefault,
})

let list: SettingResponse[]

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={client}><PhoneSettings /></QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  list = [
    s('phone_calls.enabled', false, hh([{ household_id: 'h1', household_name: 'Maple St', value: true }], 1)),
    s('phone.provider', 'sip', { options: ['twilio', 'sip'] }),
    s('phone.sip.server', 'pbx.lan'),
    s('phone.sip.transport', 'udp', { options: ['udp', 'tcp'] }),
    s('phone.sip.password', '********', { is_secret: true }),
    s('phone.twilio_auth_token', '', { is_secret: true }),
    s('phone_calls.max_call_seconds', 600, hh([], 2)),
    s('phone_calls.call_context', '********', { is_secret: true }),
    s('household.location', 'Springfield, IL', hh([], 2)),
    s('phone.inbound.enabled', false, hh([], 2)),
    s('phone_calls.audio_retention_days', 30, hh([], 2)),
  ]
  vi.mocked(getServiceSettings).mockImplementation(async () => ({
    services: [{ service_name: 'cc', success: true, settings: list, error: null, latency_ms: 1 }],
    total_services: 1, successful_services: 1, failed_services: 0,
  }))
  vi.mocked(updateSetting).mockResolvedValue({ service_name: 'cc', success: true, key: '', requires_reload: false, message: null, error: null })
})

describe('PhoneSettings', () => {
  it('groups the phone settings by task and shows the SIP line for the sip provider', async () => {
    show()
    const line = await screen.findByRole('region', { name: 'Phone line: SIP' })
    expect(within(line).getByText('PBX address')).toBeInTheDocument()
    expect(within(line).getByText('pbx.lan')).toBeInTheDocument()
    expect(within(line).queryByText('Auth token')).not.toBeInTheDocument()
    for (const name of ['Calls', 'Limits', 'Looking up businesses', 'Answering calls', 'Privacy']) {
      expect(screen.getByRole('region', { name })).toBeInTheDocument()
    }
    expect(within(screen.getByRole('region', { name: 'Limits' })).getByText('Seconds a call may last before Jarvis hangs up.')).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Privacy' })).getByText(/edited in the mobile app/)).toBeInTheDocument()
  })

  it('never puts a secret or the per-user call context in the page', async () => {
    show()
    await screen.findByRole('region', { name: 'Phone line: SIP' })
    expect(document.body.innerHTML).not.toContain('********')
    expect(screen.queryByText('phone_calls.call_context')).not.toBeInTheDocument()
    expect(screen.getByText('set (hidden)')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Edit: Extension password' }))
    expect(screen.getByLabelText('Extension password')).toHaveValue('')
  })

  it('shows Twilio settings when the provider is twilio', async () => {
    list[1] = s('phone.provider', 'twilio', { options: ['twilio', 'sip'] })
    show()
    const line = await screen.findByRole('region', { name: 'Phone line: Twilio' })
    expect(within(line).getByText('Auth token')).toBeInTheDocument()
    expect(within(line).queryByText('PBX address')).not.toBeInTheDocument()
  })

  it('saves an edit through the settings API', async () => {
    show()
    const line = await screen.findByRole('region', { name: 'Phone line: SIP' })
    fireEvent.click(within(line).getByRole('button', { name: 'Edit: PBX address' }))
    fireEvent.change(within(line).getByLabelText('PBX address'), { target: { value: 'pbx.home.arpa' } })
    fireEvent.click(within(line).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(updateSetting).toHaveBeenCalledWith('cc', 'phone.sip.server', 'pbx.home.arpa', undefined))
  })

  it('says which households have calls on', async () => {
    show()
    const calls = await screen.findByRole('region', { name: 'Calls' })
    expect(within(calls).getByText('On for Maple St')).toBeInTheDocument()
  })

  it('shows an error with a retry when the settings fail to load', async () => {
    vi.mocked(getServiceSettings).mockRejectedValueOnce(new Error('boom'))
    show()
    expect(await screen.findByText("Couldn't load the phone settings")).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('region', { name: 'Phone line: SIP' })).toBeInTheDocument()
  })
})

describe('whoIsOn', () => {
  const on = (value: boolean, values: { household_id: string; household_name: string; value: unknown }[], n: number) =>
    whoIsOn(s('phone_calls.enabled', value, hh(values, n)))
  it('covers the default and each household of its own', () => {
    expect(on(false, [], 2)).toBe('Off for every household')
    expect(on(true, [], 2)).toBe('On for every household')
    expect(on(true, [{ household_id: 'a', household_name: 'Cabin', value: false }], 1)).toBe('On for every household except Cabin')
    expect(on(true, [{ household_id: 'a', household_name: 'Cabin', value: false }], 0)).toBe('Off for every household')
    expect(on(false, [{ household_id: 'a', household_name: 'Cabin', value: true }], 3)).toBe('On for Cabin')
  })
})
