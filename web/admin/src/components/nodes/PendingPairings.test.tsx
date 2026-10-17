import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'
import { apiClient } from '@/api/client'
import type { PairingsView } from '@/api/nodes'
import { toast } from 'sonner'
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
    waiting: 1,
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
  it('lists no codes: approves the typed code into the chosen household and room', async () => {
    renderIt()
    expect(await screen.findByTestId('pairings-waiting')).toHaveTextContent(
      '1 computer is waiting for approval. Type the code shown on the computer.',
    )
    const approve = screen.getByRole('button', { name: /approve/i })
    expect(approve).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Code shown on the computer'), { target: { value: 'abcd-efgh ' } })
    fireEvent.change(screen.getByLabelText('Household'), { target: { value: 'hh2' } })
    fireEvent.change(screen.getByLabelText('Room'), { target: { value: 'study' } })
    fireEvent.click(approve)
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pairings/approve', {
        code: 'abcd-efgh',
        household_id: 'hh2',
        room: 'study',
      }),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Approved. The computer asks to confirm Cabin before it joins.'))
    expect(screen.getByLabelText('Code shown on the computer')).toHaveValue('')
  })

  it('denies the typed code', async () => {
    renderIt()
    fireEvent.change(await screen.findByLabelText('Code shown on the computer'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(screen.getByRole('button', { name: /deny/i }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pairings/deny', { code: 'ABCD-EFGH' }))
  })

  it('says how to start a pairing when nothing waits, and still takes a code', async () => {
    view = { waiting: 0, households: view.households }
    renderIt()
    expect(await screen.findByText('Pending pairings')).toBeInTheDocument()
    expect(screen.getByText(/No computers waiting/)).toHaveTextContent(
      "No computers waiting. On the computer, open Jarvis Automation Agent or run jarvis-agent enroll --server <this server's address>, then type the code it shows below.",
    )
    expect(screen.getByLabelText('Code shown on the computer')).toBeInTheDocument()
  })

  it('shows a new request on the next poll, without a reload', async () => {
    vi.useFakeTimers()
    view = { waiting: 0, households: view.households }
    renderIt()
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(screen.getByText(/No computers waiting/)).toBeInTheDocument()

    view = { waiting: 2, households: view.households }
    // usePairings' 3 s refetchInterval fires; the rest flushes react-query's and React's zero timeouts.
    await act(() => vi.advanceTimersByTimeAsync(3_100))
    expect(screen.getByTestId('pairings-waiting')).toHaveTextContent('2 computers are waiting')
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
