import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import SuppressedSuggestionsPage from './SuppressedSuggestionsPage'

const api = vi.hoisted(() => ({ listSuppressions: vi.fn(), deleteSuppression: vi.fn() }))
vi.mock('@/api/suppressions', () => api)
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SuppressedSuggestionsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('SuppressedSuggestionsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('lists what was blocked and allows one again', async () => {
    api.listSuppressions
      .mockResolvedValueOnce([
        { id: 's1', command: 'send_message', source_key: 'sam', descriptor: 'Text Sam good morning', created_at: '2026-11-10T09:00:00' },
        { id: 's2', command: 'set_thermostat', source_key: null, descriptor: null, created_at: '2026-11-11T09:00:00' },
      ])
      .mockResolvedValue([])
    api.deleteSuppression.mockResolvedValue(undefined)
    renderPage()
    expect(await screen.findByText('Text Sam good morning')).toBeInTheDocument()
    // Without a descriptor or source the command names it.
    expect(screen.getByText('set_thermostat')).toBeInTheDocument()
    expect(api.listSuppressions).toHaveBeenCalledWith('h1')

    fireEvent.click(screen.getByRole('button', { name: 'Allow again: Text Sam good morning' }))
    expect(await screen.findByText('Nothing blocked')).toBeInTheDocument()
    expect(api.deleteSuppression).toHaveBeenCalledWith('h1', 's1')
  })

  it('says when nothing is blocked', async () => {
    api.listSuppressions.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('Nothing blocked')).toBeInTheDocument()
  })
})
