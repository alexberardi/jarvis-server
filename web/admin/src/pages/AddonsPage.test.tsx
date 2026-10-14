import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { NodeLiveness } from '@/api/nodes'
import AddonsPage from './AddonsPage'

const api = vi.hoisted(() => ({ getNodeLiveness: vi.fn(), getPairings: vi.fn(), getSystemInfo: vi.fn() }))
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), ...api }))
vi.mock('@/api/system', async (orig) => ({ ...(await orig<typeof import('@/api/system')>()), getSystemInfo: api.getSystemInfo }))
vi.mock('@/api/auth', async (orig) => ({
  ...(await orig<typeof import('@/api/auth')>()),
  listHouseholds: vi.fn().mockResolvedValue([{ id: 'h1', name: 'Home', role: 'member' }]),
}))

const node = (node_id: string, room: string, online: boolean, ext?: string, hh = 'h1'): NodeLiveness => ({
  node_id,
  room,
  household_id: hh,
  online,
  last_seen: '2026-10-13T10:00:00',
  last_seen_version: null,
  is_busy: false,
  node_kind: 'desktop',
  capabilities: ext ? { browser_extension: ext } : {},
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <AddonsPage />
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
  api.getPairings.mockResolvedValue({ pairings: [], households: [{ id: 'h1', name: 'Home', role: 'member' }] })
  api.getNodeLiveness.mockResolvedValue([
    node('a', 'office', true, 'connected'),
    node('b', 'laptop', false, 'disconnected'),
    { ...node('pi', 'kitchen', true), node_kind: null },
    node('x', 'elsewhere', true, 'connected', 'h9'),
  ])
})

describe('AddonsPage', () => {
  it("lists the user's desktop agents with online and extension state", async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('office')
    expect(rows[0]).toHaveTextContent('Online')
    expect(rows[0]).toHaveTextContent('Browser extension connected')
    expect(rows[1]).toHaveTextContent('laptop')
    expect(rows[1]).toHaveTextContent('Offline')
    expect(rows[1]).toHaveTextContent('Browser extension not connected')
    expect(screen.queryByText('kitchen')).toBeNull()
    expect(screen.queryByText('elsewhere')).toBeNull()
  })

  it('offers Start only for an agent that is known but offline', async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    const [online, offline] = within(list).getAllByRole('listitem')
    expect(within(online).queryByRole('link', { name: 'Start' })).toBeNull()
    expect(within(offline).getByRole('link', { name: 'Start' })).toHaveAttribute('href', 'jarvis-agent://start')
  })

  it('links downloads and the extension without detecting anything, and never asks for system info as a member', async () => {
    renderPage()
    await screen.findByRole('list', { name: 'Your devices' })
    expect(screen.getByRole('link', { name: /Download for/ })).toHaveAttribute('href', expect.stringContaining('/releases'))
    expect(screen.getByRole('link', { name: /How browser control works/ })).toBeInTheDocument()
    expect(screen.getByText('Phone app')).toBeInTheDocument()
    expect(await screen.findByText(/No computers waiting/)).toBeInTheDocument()
    expect(api.getSystemInfo).not.toHaveBeenCalled()
  })

  it('says so when there are no computers yet', async () => {
    api.getNodeLiveness.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/No computers yet/)).toBeInTheDocument()
  })
})
