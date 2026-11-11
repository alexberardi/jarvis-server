import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import DevicesPage from './DevicesPage'

const api = vi.hoisted(() => ({ getDeviceConfig: vi.fn(), listDevices: vi.fn(), listDeviceRooms: vi.fn(), requestDeviceJob: vi.fn(), importDevices: vi.fn(), updateDevice: vi.fn(), removeDevice: vi.fn(), getDeviceState: vi.fn(), controlDevice: vi.fn() }))
const auth = vi.hoisted(() => ({ activeHouseholdId: 'h1', state: { user: { id: 1, is_superuser: false } } }))
vi.mock('@/api/devices', async (orig) => ({ ...(await orig<typeof import('@/api/devices')>()), ...api }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => auth }))

const lamp = { id: 'd1', household_id: 'h1', entity_id: 'light.desk', name: 'Desk lamp', domain: 'light', protocol: 'demo', room_id: 'r1', room_name: 'Study', source: 'direct', is_controllable: true, supported_actions: [], node_id: 'n1' }
const found = { entity_id: 'climate.hall', name: 'Hall thermostat', domain: 'climate', protocol: 'demo', already_registered: false, existing_device_id: null }
const config = { primary_node_id: 'offline', use_external_devices: false, nodes: [{ node_id: 'offline', name: 'Kitchen Pi', online: false }, { node_id: 'n1', name: 'Study computer', online: true, node_kind: 'desktop' }] }

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeHouseholdId = 'h1'
  api.getDeviceConfig.mockResolvedValue(config)
  api.listDevices.mockResolvedValue([lamp])
  api.listDeviceRooms.mockResolvedValue([{ id: 'r1', name: 'Study' }])
  api.getDeviceState.mockResolvedValue({ state: {}, ui_hints: {}, error: null })
  api.requestDeviceJob.mockResolvedValue({ status: 'completed', devices: [found, { ...lamp, already_registered: true, existing_device_id: 'd1' }] })
  api.importDevices.mockResolvedValue({ created: 1, updated: 0 })
  api.updateDevice.mockResolvedValue(lamp)
  api.removeDevice.mockResolvedValue(undefined)
})

function view() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const content = () => <QueryClientProvider client={qc}><MemoryRouter><DevicesPage /></MemoryRouter></QueryClientProvider>
  const result = render(content())
  return { ...result, refresh: () => result.rerender(content()) }
}

it('lists and searches saved devices without scanning on page load', async () => {
  view()
  expect(await screen.findByRole('button', { name: 'Manage Desk lamp' })).toBeVisible()
  expect(screen.getByText(/Study · light · demo/)).toBeVisible()
  expect(api.requestDeviceJob).not.toHaveBeenCalled()
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'missing' } })
  expect(screen.getByText('No matching devices')).toBeVisible()
})

it('refreshes the registry when discovery finds a device added by another client', async () => {
  const newer = { ...lamp, id: 'new', name: 'New lamp' }
  api.listDevices.mockResolvedValueOnce([lamp]).mockResolvedValue([lamp, newer])
  api.requestDeviceJob.mockResolvedValue({ status: 'completed', devices: [{ ...newer, already_registered: true, existing_device_id: 'new' }] })
  view()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Add devices' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Add devices' }))
  fireEvent.click(screen.getByRole('button', { name: 'Scan for devices' }))
  fireEvent.click(await screen.findByRole('button', { name: 'View added' }))
  expect(await screen.findByRole('dialog', { name: 'New lamp' })).toBeVisible()
})

it('scans with an online desktop, skips registered devices and imports named selections', async () => {
  view()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Add devices' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Add devices' }))
  expect(screen.getByLabelText('Scan using')).toHaveValue('n1')
  fireEvent.click(screen.getByRole('button', { name: 'Scan for devices' }))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Select Hall thermostat' }))
  expect(screen.getByRole('checkbox', { name: 'Select Desk lamp' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Name for Hall thermostat'), { target: { value: 'Hall heating' } })
  fireEvent.change(screen.getByLabelText('Room for Hall thermostat'), { target: { value: 'r1' } })
  fireEvent.click(screen.getByRole('button', { name: 'Add selected (1)' }))
  await waitFor(() => expect(api.importDevices).toHaveBeenCalledWith('h1', 'n1', [expect.objectContaining({ entity_id: 'climate.hall', name: 'Hall heating', room_id: 'r1', source: 'direct' })], expect.any(AbortSignal)))
})

it('keeps saved devices on pairing failure and follows repeated PIN prompts', async () => {
  api.getDeviceState.mockResolvedValue({ state: { needs_pairing: true }, ui_hints: { control_type: 'pairing' } })
  const prompt = { type: 'pin', prompt: 'Enter the setup code', session_id: 's1', follow_up_action: 'pair_finish' }
  api.controlDevice.mockResolvedValueOnce({ success: false, input_required: prompt })
    .mockResolvedValueOnce({ success: false, input_required: { ...prompt, session_id: 's2' } })
    .mockResolvedValueOnce({ success: true })
  view()
  fireEvent.click(await screen.findByRole('button', { name: 'Manage Desk lamp' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Pair device' }))
  expect(await screen.findByLabelText('Pairing code')).toHaveFocus()
  fireEvent.change(screen.getByLabelText('Pairing code'), { target: { value: '012-345' } })
  fireEvent.click(screen.getByRole('button', { name: 'Submit code' }))
  await waitFor(() => expect(api.controlDevice).toHaveBeenCalledWith('h1', 'd1', 'pair_finish', { session_id: 's1', pin: '012-345' }, expect.any(AbortSignal)))
  await waitFor(() => expect(screen.getByLabelText('Pairing code')).toHaveValue(''))
  api.getDeviceState.mockResolvedValue({ state: { temperature: 20 }, ui_hints: {}, error: null })
  fireEvent.change(screen.getByLabelText('Pairing code'), { target: { value: '0987' } })
  fireEvent.click(screen.getByRole('button', { name: 'Submit code' }))
  expect(await screen.findByText('Pairing completed.')).toBeVisible()
  expect(api.removeDevice).not.toHaveBeenCalled()
})

it('ignores late discovery results after a household switch', async () => {
  let finish!: (value: unknown) => void
  api.requestDeviceJob.mockReturnValue(new Promise((resolve) => { finish = resolve }))
  const page = view()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Add devices' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Add devices' }))
  fireEvent.click(screen.getByRole('button', { name: 'Scan for devices' }))
  const signal = api.requestDeviceJob.mock.calls[0][2] as AbortSignal
  auth.activeHouseholdId = 'h2'
  page.refresh()
  await act(async () => finish({ status: 'completed', devices: [found] }))
  expect(signal.aborted).toBe(true)
  expect(screen.queryByText('Hall thermostat')).not.toBeInTheDocument()
  expect(api.importDevices).not.toHaveBeenCalled()
})

it('shows manager devices read-only in external mode', async () => {
  api.getDeviceConfig.mockResolvedValue({ ...config, use_external_devices: true })
  api.requestDeviceJob.mockResolvedValue({ status: 'completed', manager_name: 'all', can_edit_devices: false, devices: [{ ...found, source: 'home_assistant', area: 'Hall' }] })
  view()
  expect(await screen.findByText('Hall thermostat')).toBeVisible()
  expect(api.requestDeviceJob).toHaveBeenCalledWith('n1', 'device-list', expect.any(AbortSignal))
  expect(screen.queryByRole('button', { name: 'Add devices' })).not.toBeInTheDocument()
  expect(api.importDevices).not.toHaveBeenCalled()
})

it('saves names and rooms and requires confirmation before removing a device', async () => {
  view()
  fireEvent.click(await screen.findByRole('button', { name: 'Manage Desk lamp' }))
  fireEvent.change(screen.getByLabelText('Device name'), { target: { value: 'Reading lamp' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(api.updateDevice).toHaveBeenCalledWith('h1', 'd1', { name: 'Reading lamp', room_id: 'r1' }, expect.any(AbortSignal)))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: 'Manage Desk lamp' }))
  fireEvent.click(screen.getByRole('button', { name: 'Remove device' }))
  expect(api.removeDevice).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Remove from Jarvis' }))
  await waitFor(() => expect(api.removeDevice).toHaveBeenCalledWith('h1', 'd1', expect.any(AbortSignal)))
})

it('keeps a failed pairing recoverable and cancels pending answers on close', async () => {
  api.getDeviceState.mockResolvedValue({ state: { needs_pairing: true }, ui_hints: {} })
  api.controlDevice.mockResolvedValueOnce({ success: false, error: 'The code was rejected. Start again.' })
  view()
  fireEvent.click(await screen.findByRole('button', { name: 'Manage Desk lamp' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Pair device' }))
  expect(await screen.findByText('The code was rejected. Start again.')).toBeVisible()
  expect(api.removeDevice).not.toHaveBeenCalled()
  let finish!: (value: unknown) => void
  api.controlDevice.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  fireEvent.click(screen.getByRole('button', { name: 'Pair device' }))
  const signal = api.controlDevice.mock.calls.at(-1)![4] as AbortSignal
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  await act(async () => finish({ success: false, input_required: { type: 'pin', session_id: 'late', follow_up_action: 'pair_finish' } }))
  expect(signal.aborted).toBe(true)
  expect(screen.queryByLabelText('Pairing code')).not.toBeInTheDocument()
})

it('refreshes saved devices after an ambiguous import and requires a new scan', async () => {
  api.importDevices.mockRejectedValueOnce(new Error('Connection lost'))
  view()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Add devices' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Add devices' }))
  fireEvent.click(screen.getByRole('button', { name: 'Scan for devices' }))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Select Hall thermostat' }))
  fireEvent.click(screen.getByRole('button', { name: 'Add selected (1)' }))
  expect(await screen.findByText(/Could not confirm the import/)).toBeVisible()
  expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Add selected (0)' })).toBeDisabled()
  await waitFor(() => expect(api.listDevices).toHaveBeenCalledTimes(2))
})

it('explains an empty scan and refuses unsupported pairing inputs', async () => {
  api.requestDeviceJob.mockResolvedValue({ status: 'completed', devices: [] })
  api.getDeviceState.mockResolvedValue({ state: { needs_pairing: true }, ui_hints: {} })
  api.controlDevice.mockResolvedValueOnce({ success: false, input_required: { type: 'confirm', prompt: 'Confirm in app', session_id: 's1', follow_up_action: 'confirm' } })
  view()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Add devices' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Add devices' }))
  fireEvent.click(screen.getByRole('button', { name: 'Scan for devices' }))
  expect(await screen.findByText('No devices found')).toBeVisible()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Manage Desk lamp' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Pair device' }))
  expect(await screen.findByText(/unsupported setup step/)).toBeVisible()
  expect(screen.queryByLabelText('Pairing code')).not.toBeInTheDocument()
})
