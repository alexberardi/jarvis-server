import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'
import { apiClient } from '@/api/client'
import type { PairingsView } from '@/api/nodes'
import PendingPairings from './PendingPairings'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}))

const get = vi.spyOn(apiClient, 'get')
const post = vi.spyOn(apiClient, 'post')

let view: PairingsView

function renderIt() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <PendingPairings />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  view = {
    pairings: [
      {
        code: 'ABCD-EFGH',
        name: 'Office-PC',
        platform: 'linux',
        requested_from: '192.0.2.7',
        created_at: '2026-10-13T12:00:00Z',
        expires_in: 540,
      },
    ],
    households: [
      { id: 'hh1', name: 'Home', role: 'owner' },
      { id: 'hh2', name: 'Cabin', role: 'member' },
    ],
  }
  get.mockImplementation(async (url: string) => {
    if (url === '/api/cc/api/v0/nodes/pairings') return { data: view }
    throw new Error(`unexpected GET ${url}`)
  })
  post.mockResolvedValue({ data: {} })
})

describe('PendingPairings', () => {
  it('shows the code and approves into the chosen household and room', async () => {
    renderIt()
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    expect(screen.getByText('Office-PC')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Household'), { target: { value: 'hh2' } })
    fireEvent.change(screen.getByLabelText('Room'), { target: { value: 'study' } })
    fireEvent.click(screen.getByRole('button', { name: /approve/i }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pairings/approve', {
        code: 'ABCD-EFGH',
        household_id: 'hh2',
        room: 'study',
      }),
    )
  })

  it('denies by code', async () => {
    renderIt()
    fireEvent.click(await screen.findByRole('button', { name: /deny/i }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pairings/deny', { code: 'ABCD-EFGH' }))
  })

  it('renders nothing when nothing waits', async () => {
    view = { pairings: [], households: view.households }
    renderIt()
    await waitFor(() => expect(get).toHaveBeenCalled())
    expect(screen.queryByText('Pending pairings')).toBeNull()
  })

  it('renders nothing for someone in no household (cc answers 403)', async () => {
    get.mockRejectedValue(
      new AxiosError('forbidden', '403', undefined, undefined, {
        status: 403,
        statusText: 'Forbidden',
        data: { detail: 'User is not a member of any household' },
        headers: {},
        config: { headers: new AxiosHeaders() },
      }),
    )
    renderIt()
    await waitFor(() => expect(get).toHaveBeenCalled())
    expect(screen.queryByText('Pending pairings')).toBeNull()
  })
})
