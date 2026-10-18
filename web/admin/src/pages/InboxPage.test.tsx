import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { InboxItem } from '@/api/inbox'
import InboxPage from './InboxPage'
import InboxItemPage from './InboxItemPage'
import { useUnreadCount } from '@/hooks/useInbox'

/** The sidebar's unread badge, as Sidebar.tsx reads it. */
function Badge() {
  const unread = useUnreadCount()
  return <output data-testid="badge">{unread.data ?? ''}</output>
}

const api = vi.hoisted(() => ({
  listInbox: vi.fn(),
  getInboxItem: vi.fn(),
  markRead: vi.fn(),
  deleteItem: vi.fn(),
  bulkRead: vi.fn(),
  bulkDelete: vi.fn(),
  sendNodeAction: vi.fn(),
  unreadCount: vi.fn(),
}))
vi.mock('@/api/inbox', async (orig) => ({ ...(await orig<typeof import('@/api/inbox')>()), ...api }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), sendNodeAction: api.sendNodeAction }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), listHouseholds: vi.fn().mockResolvedValue([]) }))

const it1 = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: 'r1',
  user_id: 7,
  household_id: 'h1',
  title: 'Best coffee beans',
  summary: 'A short summary',
  body: '## Findings\n\n- **Ethiopia** <script>alert(1)</script>',
  category: 'deep_research',
  source_service: 'cc',
  metadata: { sources: [{ title: 'Roast mag', url: 'https://roast.example/a' }], elapsed_seconds: 42, pages_scraped: 5, pages_attempted: 6 },
  is_read: false,
  created_at: '2026-10-13T10:00:00',
  ...over,
})

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter initialEntries={[path]}>
          <Badge />
          <Routes>
            <Route path="/inbox" element={<InboxPage />} />
            <Route path="/inbox/:id" element={<InboxItemPage />} />
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', 'a.e30.s')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', is_superuser: false }))
  api.markRead.mockResolvedValue(undefined)
  api.unreadCount.mockResolvedValue(0)
  api.bulkRead.mockResolvedValue(undefined)
  api.bulkDelete.mockResolvedValue(undefined)
})

describe('InboxPage (list)', () => {
  it('lists items with unread marks and links to each', async () => {
    api.listInbox.mockResolvedValue([it1(), it1({ id: 'r2', title: 'Old note', is_read: true, category: 'notice' })])
    renderAt('/inbox')
    expect(await screen.findByText('Best coffee beans')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Best coffee beans/ })).toHaveAttribute('href', '/inbox/r1')
    expect(screen.getAllByLabelText('Unread')).toHaveLength(1)
    expect(screen.getByText('deep research')).toBeInTheDocument()
  })

  it('selects items and marks them read or deletes them in bulk', async () => {
    api.listInbox.mockResolvedValue([it1(), it1({ id: 'r2', title: 'Second' })])
    renderAt('/inbox')
    await screen.findByText('Second')
    fireEvent.click(screen.getByRole('button', { name: /Select/ }))
    fireEvent.click(screen.getByLabelText('Select Second'))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Mark read/ }))
    })
    expect(api.bulkRead).toHaveBeenCalledWith(['r2'])
    fireEvent.click(screen.getByRole('button', { name: /Select/ }))
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Delete/ }))
    })
    expect(api.bulkDelete).toHaveBeenCalledWith(['r1', 'r2'])
  })

  it('has an empty state', async () => {
    api.listInbox.mockResolvedValue([])
    renderAt('/inbox')
    expect(await screen.findByText(/Nothing here yet/)).toBeInTheDocument()
  })
})

describe('InboxItemPage (detail)', () => {
  it('renders the body as markdown without raw HTML, the sources, and marks it read', async () => {
    api.getInboxItem.mockResolvedValue(it1())
    renderAt('/inbox/r1')
    expect(await screen.findByRole('heading', { name: 'Findings' })).toBeInTheDocument()
    expect(screen.getByText('Ethiopia').tagName).toBe('STRONG')
    expect(document.querySelector('script')).toBeNull()
    const src = screen.getByRole('link', { name: /Roast mag/ })
    expect(src).toHaveAttribute('href', 'https://roast.example/a')
    expect(src).toHaveAttribute('rel', expect.stringContaining('noopener'))
    expect(screen.getByText(/Researched in 42s · 5\/6 pages/)).toBeInTheDocument()
    expect(api.markRead).toHaveBeenCalledWith('r1')
  })

  it('refreshes the unread badge when opening the item read it on the server', async () => {
    // GET /inbox/{id} marks the item read itself (legacy parity), so it arrives already read.
    api.unreadCount.mockResolvedValueOnce(2).mockResolvedValue(1)
    api.getInboxItem.mockResolvedValue(it1({ is_read: true }))
    renderAt('/inbox/r1')
    expect(await screen.findByRole('heading', { name: 'Best coffee beans' })).toBeInTheDocument()
    await vi.waitFor(() => expect(screen.getByTestId('badge')).toHaveTextContent('1'))
  })

  it("runs a confirmation's button on its node", async () => {
    api.getInboxItem.mockResolvedValue(
      it1({
        category: 'confirmation',
        is_read: true,
        metadata: { node_id: 'n1', command_name: 'send_email', draft: 'hi', actions: [{ button_text: 'Send', button_action: 'send', button_type: 'primary' }] },
      }),
    )
    api.sendNodeAction.mockResolvedValue({ status: 'completed', success: true })
    renderAt('/inbox/r1')
    const box = await screen.findByRole('button', { name: 'Send' })
    await act(async () => {
      fireEvent.click(box)
    })
    expect(api.sendNodeAction).toHaveBeenCalledWith('n1', { command_name: 'send_email', action_name: 'send', context: { draft: 'hi' } })
    expect(within(document.body).getByText('Done!')).toBeInTheDocument()
    expect(api.markRead).not.toHaveBeenCalled()
  })
})
