import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { inboxKeys, useInboxAlerts } from './useInbox'

const api = vi.hoisted(() => ({ unreadCount: vi.fn(), listInbox: vi.fn() }))
vi.mock('@/api/inbox', async (orig) => ({ ...(await orig<typeof import('@/api/inbox')>()), ...api }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

function Badge() {
  const unread = useInboxAlerts()
  return <output data-testid="badge">{unread.data ?? ''}</output>
}

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/']}>
        <Badge />
        <Toaster />
        <Routes>
          <Route path="/" element={null} />
          <Route path="/inbox/:id" element={<h1>Item page</h1>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { poll: () => act(() => qc.invalidateQueries({ queryKey: inboxKeys.unread('h1') })) }
}

describe('useInboxAlerts', () => {
  beforeEach(() => vi.clearAllMocks())

  it('the first count is the baseline: nothing toasts for items already there', async () => {
    api.unreadCount.mockResolvedValue(4)
    setup()
    await vi.waitFor(() => expect(screen.getByTestId('badge')).toHaveTextContent('4'))
    expect(api.listInbox).not.toHaveBeenCalled()
  })

  it('toasts each new item when the count rises, and Open goes to it', async () => {
    api.unreadCount.mockResolvedValueOnce(1).mockResolvedValue(3)
    api.listInbox.mockResolvedValue([
      { id: 'n2', title: 'Your research is ready', summary: 'Best coffee beans' },
      { id: 'n1', title: 'Front door left open', summary: '' },
    ])
    const { poll } = setup()
    await vi.waitFor(() => expect(screen.getByTestId('badge')).toHaveTextContent('1'))
    await poll()
    expect(await screen.findByText('Your research is ready')).toBeInTheDocument()
    expect(screen.getByText('Front door left open')).toBeInTheDocument()
    expect(api.listInbox).toHaveBeenCalledWith({ limit: 2, is_read: false })

    fireEvent.click(screen.getAllByRole('button', { name: 'Open' })[0])
    expect(await screen.findByRole('heading', { name: 'Item page' })).toBeInTheDocument()
  })

  it('a falling count (items read elsewhere) toasts nothing', async () => {
    api.unreadCount.mockResolvedValueOnce(3).mockResolvedValue(1)
    const { poll } = setup()
    await vi.waitFor(() => expect(screen.getByTestId('badge')).toHaveTextContent('3'))
    await poll()
    await vi.waitFor(() => expect(screen.getByTestId('badge')).toHaveTextContent('1'))
    expect(api.listInbox).not.toHaveBeenCalled()
  })
})
