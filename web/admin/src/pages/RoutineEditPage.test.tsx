import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import RoutineEditPage from './RoutineEditPage'

const api = vi.hoisted(() => ({
  getRoutine: vi.fn(), createRoutine: vi.fn(), updateRoutine: vi.fn(), deleteRoutine: vi.fn(),
  getDeviceConfig: vi.fn(), listChatNodes: vi.fn(), getNodeTools: vi.fn(), listDevices: vi.fn(), listDeviceRooms: vi.fn(),
}))
vi.mock('@/api/routines', async (orig) => ({ ...(await orig<typeof import('@/api/routines')>()), getRoutine: api.getRoutine, createRoutine: api.createRoutine, updateRoutine: api.updateRoutine, deleteRoutine: api.deleteRoutine }))
vi.mock('@/api/devices', async (orig) => ({ ...(await orig<typeof import('@/api/devices')>()), getDeviceConfig: api.getDeviceConfig, listDevices: api.listDevices, listDeviceRooms: api.listDeviceRooms }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), listChatNodes: api.listChatNodes, getNodeTools: api.getNodeTools }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/routines" element={<h1>Routines list</h1>} />
          <Route path="/routines/new" element={<RoutineEditPage />} />
          <Route path="/routines/:id" element={<RoutineEditPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const status = () => screen.getByRole('status')

describe('RoutineEditPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getDeviceConfig.mockResolvedValue({ primary_node_id: 'kitchen', use_external_devices: false, nodes: [{ node_id: 'kitchen', name: null, room: 'Kitchen', online: true }] })
    api.listChatNodes.mockResolvedValue([{ node_id: 'kitchen', capabilities: { simple_routines: true } }])
    api.listDevices.mockResolvedValue([{ name: 'Porch light', entity_id: 'light.porch', domain: 'light' }])
    api.listDeviceRooms.mockResolvedValue([])
    api.getNodeTools.mockResolvedValue({ client_tools: [], available_commands: [
      { command_name: 'control_device', description: 'Control a device', parameters: [
        { name: 'device_name', type: 'str', required: true }, { name: 'action', type: 'str', required: true }, { name: 'brightness', type: 'int' },
      ] },
      { command_name: 'get_weather', description: 'The weather', parameters: [] },
    ] })
    api.createRoutine.mockResolvedValue({ id: 'new' })
    api.updateRoutine.mockResolvedValue({ id: 'r1' })
  })

  it('builds an Advanced routine from the node’s commands, scheduled on weekdays', async () => {
    renderAt('/routines/new')
    fireEvent.click(screen.getByLabelText('Advanced'))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Porch at dusk' } })
    expect(status()).toHaveTextContent('Add at least one trigger phrase')
    fireEvent.change(screen.getByLabelText('Trigger phrases'), { target: { value: 'porch on' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    expect(status()).toHaveTextContent('Add at least one step')

    fireEvent.click(screen.getByRole('button', { name: 'Add step' }))
    const step = screen.getByRole('region', { name: 'Step 1' })
    // The command list comes from the kitchen node's report.
    const command = await within(step).findByRole('combobox', { name: 'Command' })
    fireEvent.change(command, { target: { value: 'control_device' } })
    // Its required parameters are added; the device suggestions are the household's devices.
    const device = within(step).getByLabelText('device_name')
    await vi.waitFor(() => expect(device.getAttribute('list')).toBeTruthy())
    expect(document.getElementById(device.getAttribute('list')!)?.querySelector('option')).toHaveAttribute('value', 'Porch light')
    expect(status()).toHaveTextContent('Step 1: “device_name” is required')
    fireEvent.change(device, { target: { value: 'Porch light' } })
    fireEvent.change(within(step).getByLabelText('action'), { target: { value: 'turn_on' } })

    fireEvent.click(screen.getByRole('switch', { name: /Run on a schedule/ }))
    fireEvent.change(screen.getByLabelText('Time'), { target: { value: '19:30' } })
    fireEvent.click(screen.getByRole('button', { name: 'Weekdays' }))
    expect(screen.queryByRole('status')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Create routine' }))

    expect(await screen.findByRole('heading', { name: 'Routines list' })).toBeInTheDocument()
    expect(api.createRoutine).toHaveBeenCalledWith('h1', expect.objectContaining({
      mode: 'advanced',
      name: 'Porch at dusk',
      trigger_phrases: ['porch on'],
      steps: [{ command: 'control_device', label: 'control_device', args: [{ key: 'device_name', value: 'Porch light' }, { key: 'action', value: 'turn_on' }] }],
      schedule: expect.objectContaining({ type: 'cron', cron: '30 19 * * 1,2,3,4,5', target_node_id: 'kitchen', enabled: true }),
    }))
  })

  it('a Simple routine is one instruction', async () => {
    renderAt('/routines/new')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Movie night' } })
    fireEvent.change(screen.getByLabelText('Trigger phrases'), { target: { value: 'movie time' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    fireEvent.change(screen.getByLabelText('What should Jarvis do?'), { target: { value: 'Dim the lights to 20%' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create routine' }))
    await vi.waitFor(() => expect(api.createRoutine).toHaveBeenCalledWith('h1', expect.objectContaining({ mode: 'simple', utterance: 'Dim the lights to 20%', steps: [], schedule: null })))
  })

  it('edits a routine in place, keeping its schedule’s time zone', async () => {
    api.getRoutine.mockResolvedValue({
      id: 'r1', mode: 'advanced', name: 'Good morning', trigger_phrases: ['good morning'],
      steps: [{ command: 'get_weather', label: 'weather', args: [] }], response_instruction: 'Be brief', response_length: 'medium',
      schedule: { type: 'interval', interval_seconds: 7200, timezone: 'Europe/Paris', enabled: true, target_node_id: 'kitchen' },
    })
    renderAt('/routines/r1')
    const name = await screen.findByLabelText('Name')
    expect(name).toHaveValue('Good morning')
    expect(screen.getByLabelText('Every')).toHaveValue('120')
    fireEvent.change(name, { target: { value: 'Morning' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updateRoutine).toHaveBeenCalledWith('h1', 'r1', expect.objectContaining({
      name: 'Morning', response_length: 'medium',
      schedule: expect.objectContaining({ type: 'interval', interval_seconds: 7200, timezone: 'Europe/Paris' }),
    })))
  })

  it('reorders steps with the move buttons', async () => {
    api.getRoutine.mockResolvedValue({
      id: 'r1', mode: 'advanced', name: 'Two', trigger_phrases: ['two'], response_instruction: '', response_length: 'short', schedule: null,
      steps: [{ command: 'get_weather', label: 'first', args: [] }, { command: 'get_weather', label: 'second', args: [] }],
    })
    renderAt('/routines/r1')
    fireEvent.click(await screen.findByRole('button', { name: 'Move step 2 up' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updateRoutine.mock.calls[0][2].steps.map((s: { label: string }) => s.label)).toEqual(['second', 'first']))
  })
})
