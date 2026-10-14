import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    if (url === '/api/system/info') return { data: { listeners: [{ name: 'config', port: 7700 }] } }
    throw new Error(`unexpected GET ${url}`)
  })
  post.mockResolvedValue({ data: {} })
})

afterEach(() => {
  vi.useRealTimers()
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

  it('says how to start a pairing when nothing waits', async () => {
    view = { pairings: [], households: view.households }
    renderIt()
    expect(await screen.findByText('Pending pairings')).toBeInTheDocument()
    expect(screen.getByText(/No computers waiting/)).toHaveTextContent(
      "No computers waiting. On the computer, open Jarvis Automation Agent or run jarvis-agent enroll --server <this server's address>; its code appears here.",
    )
    expect(screen.queryByRole('button', { name: /approve/i })).toBeNull()
  })

  it('shows a new code on the next poll, without a reload', async () => {
    vi.useFakeTimers()
    const pending = view.pairings
    view = { pairings: [], households: view.households }
    renderIt()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByText(/No computers waiting/)).toBeInTheDocument()

    view = { pairings: pending, households: view.households }
    // usePairings' 3 s refetchInterval fires; the rest flushes react-query's and React's zero timeouts.
    await act(() => vi.advanceTimersByTimeAsync(3_100))
    expect(screen.getByText('ABCD-EFGH')).toBeInTheDocument()
    expect(screen.queryByText(/No computers waiting/)).toBeNull()
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
