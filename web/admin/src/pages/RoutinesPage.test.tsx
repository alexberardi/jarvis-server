import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import RoutinesPage from './RoutinesPage'

const api = vi.hoisted(() => ({ listRoutines: vi.fn(), runRoutineNow: vi.fn(), deleteRoutine: vi.fn(), getDeviceConfig: vi.fn(), listChatNodes: vi.fn() }))
vi.mock('@/api/routines', async (orig) => ({ ...(await orig<typeof import('@/api/routines')>()), listRoutines: api.listRoutines, runRoutineNow: api.runRoutineNow, deleteRoutine: api.deleteRoutine }))
vi.mock('@/api/devices', async (orig) => ({ ...(await orig<typeof import('@/api/devices')>()), getDeviceConfig: api.getDeviceConfig }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), listChatNodes: api.listChatNodes }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

const morning = {
  id: 'r1', mode: 'advanced', name: 'Good morning', trigger_phrases: ['good morning', 'morning'],
  steps: [{ command: 'get_weather', label: 'get_weather', args: [] }, { command: 'control_device', label: 'lights', args: [] }],
  response_instruction: '', response_length: 'short',
  schedule: { type: 'cron', cron: '0 7 * * 1,2,3,4,5', timezone: 'America/New_York', enabled: true, target_node_id: 'kitchen' },
}
const lights = { id: 'r2', mode: 'simple', utterance: 'lights on', name: 'Lights', trigger_phrases: ['lights'], steps: [], response_instruction: '', response_length: 'short', schedule: null }

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <RoutinesPage />
        <Toaster />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('RoutinesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listRoutines.mockResolvedValue([morning, lights])
    api.getDeviceConfig.mockResolvedValue({ primary_node_id: 'kitchen', use_external_devices: false, nodes: [
      { node_id: 'kitchen', name: null, room: 'Kitchen', online: true },
      { node_id: 'office', name: 'Office PC', room: null, online: true },
    ] })
    api.listChatNodes.mockResolvedValue([{ node_id: 'kitchen', capabilities: { simple_routines: true } }, { node_id: 'office', capabilities: {} }])
  })

  it('lists routines with phrases, kind and schedule', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: 'Good morning' })).toHaveAttribute('href', '/routines/r1')
    expect(screen.getByText('“good morning”, “morning”')).toBeInTheDocument()
    expect(screen.getByText('2 steps')).toBeInTheDocument()
    expect(screen.getByText(/^Weekdays · 7:00/)).toBeInTheDocument()
    expect(screen.getByText('Simple')).toBeInTheDocument()
  })

  it('runs a Simple routine on the only node that can, and an Advanced one where you pick', async () => {
    api.runRoutineNow.mockResolvedValue({ success: true, status: 'success', message: 'Lights are on.' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run now: Lights' }))
    expect(await screen.findByText('Lights are on.')).toBeInTheDocument()
    expect(api.runRoutineNow).toHaveBeenCalledWith('h1', 'r2', 'kitchen')

    fireEvent.click(screen.getByRole('button', { name: 'Run now: Good morning' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Run on'), { target: { value: 'office' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Run now' }))
    await vi.waitFor(() => expect(api.runRoutineNow).toHaveBeenLastCalledWith('h1', 'r1', 'office'))
  })

  it('a node that did not answer says so', async () => {
    api.listRoutines.mockResolvedValue([lights])
    api.runRoutineNow.mockResolvedValue({ success: false, status: 'timeout', message: null })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run now: Lights' }))
    expect(await screen.findByText('The node did not answer. Some steps may have run.')).toBeInTheDocument()
  })

  it('Delete asks first', async () => {
    api.deleteRoutine.mockResolvedValue(undefined)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Lights' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete routine' }))
    await vi.waitFor(() => expect(api.deleteRoutine).toHaveBeenCalledWith('h1', 'r2'))
  })
})
