import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import ErrandsPage from './ErrandsPage'

const api = vi.hoisted(() => ({ listSchedules: vi.fn(), cancelSchedule: vi.fn() }))
vi.mock('@/api/schedules', () => api)
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

const weather = { id: 's1', intent: 'Check the weather', state: 'scheduled', next_fire_at: '2026-11-17T13:00:00', is_recurring: true, cadence: 'every day at 8 AM', next_local: 'Tue Nov 17, 8 AM', description: '' }
const once = { id: 's2', intent: 'Book the dentist', state: 'scheduled', next_fire_at: '2026-11-18T14:00:00', is_recurring: false, cadence: 'once', next_local: '', description: '' }

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ErrandsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('ErrandsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('lists errands with their cadence and next run', async () => {
    api.listSchedules.mockResolvedValue([weather, once])
    renderPage()
    expect(await screen.findByText('Check the weather')).toBeInTheDocument()
    expect(screen.getByText('every day at 8 AM · next Tue Nov 17, 8 AM')).toBeInTheDocument()
    expect(screen.getByText('once')).toBeInTheDocument()
    expect(api.listSchedules).toHaveBeenCalledWith('h1')
  })

  it('Stop asks first, Keep it leaves it, Stop errand cancels it', async () => {
    api.listSchedules.mockResolvedValue([weather])
    api.cancelSchedule.mockResolvedValue([])
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Stop: Check the weather' }))
    let dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('It will stop repeating.')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep it' }))
    expect(api.cancelSchedule).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Stop: Check the weather' }))
    dialog = screen.getByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Stop errand' }))
    expect(await screen.findByText('No scheduled errands')).toBeInTheDocument()
    expect(api.cancelSchedule).toHaveBeenCalledWith('h1', 's1')
  })

  it('says how to make one when there are none', async () => {
    api.listSchedules.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('No scheduled errands')).toBeInTheDocument()
  })
})
