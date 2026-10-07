import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as api from '@/api/connections'
import type { ConnectionsResponse } from '@/api/connections'
import ConnectionsPage from './ConnectionsPage'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('@/api/connections', () => ({
  getConnections: vi.fn(),
  addService: vi.fn(),
  removeService: vi.fn(),
  createApp: vi.fn(),
  rotateApp: vi.fn(),
  revokeApp: vi.fn(),
}))

const m = vi.mocked(api)

function response(withHealth: boolean): ConnectionsResponse {
  const h = withHealth ? { healthy: true, latency_ms: 3 } : null
  return {
    listeners: [{ name: 'jarvis-auth', url: 'http://10.0.0.5:7701', port: 7701, managed: 'listener', health: h }],
    external: [
      { name: 'jarvis-pantry', url: 'https://pantry.example', health_path: '/health', description: '', managed: 'setting', removable: false, health: h },
      { name: 'jarvis-recipes', url: 'http://10.0.0.9:7030', health_path: '/health', description: 'recipes', managed: '', removable: true, health: h },
    ],
    apps: [{ app_id: 'jarvis-recipes', name: 'Recipes', is_active: true, created_at: '2026-10-07T00:00:00Z', last_rotated_at: null }],
  }
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ConnectionsPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  m.getConnections.mockImplementation(async (health) => response(health))
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})

describe('ConnectionsPage (AD7)', () => {
  it('lists without health first, then probes', async () => {
    renderPage()
    expect(await screen.findByText('jarvis-auth')).toBeInTheDocument()
    expect(m.getConnections).toHaveBeenCalledWith(false)
    expect(m.getConnections).toHaveBeenCalledWith(true)
    await waitFor(() => expect(screen.getAllByText(/healthy/).length).toBeGreaterThan(0))
  })

  it('offers delete only on removable entries', async () => {
    renderPage()
    const pantry = await screen.findByTestId('external-jarvis-pantry')
    expect(within(pantry).queryByRole('button', { name: /Remove/ })).toBeNull()
    const recipes = screen.getByTestId('external-jarvis-recipes')
    fireEvent.click(within(recipes).getByRole('button', { name: 'Remove jarvis-recipes' }))
    await waitFor(() => expect(m.removeService).toHaveBeenCalledWith('jarvis-recipes'))
  })

  it('shows a new app key once, in a modal, and forgets it on close', async () => {
    m.createApp.mockResolvedValue({ app_id: 'satellite', name: 'GPU box', app_key: 'sekrit-key-123', is_active: true })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /New app/ }))
    fireEvent.change(screen.getByLabelText('App ID'), { target: { value: 'satellite' } })
    fireEvent.change(screen.getByLabelText('App name'), { target: { value: 'GPU box' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    const dialog = await screen.findByRole('dialog')
    expect(m.createApp).toHaveBeenCalledWith('satellite', 'GPU box')
    expect(within(dialog).getByTestId('app-key')).toHaveTextContent('sekrit-key-123')
    expect(within(dialog).getByText(/only time it is shown/)).toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: "I've stored it" }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByText('sekrit-key-123')).toBeNull()
  })

  it('rotating shows the new key once', async () => {
    m.rotateApp.mockResolvedValue({ app_id: 'jarvis-recipes', app_key: 'rotated-456', is_active: true, last_rotated_at: null })
    renderPage()
    const row = await screen.findByTestId('app-jarvis-recipes')
    fireEvent.click(within(row).getByRole('button', { name: /Rotate key/ }))
    expect(await screen.findByTestId('app-key')).toHaveTextContent('rotated-456')
  })
})
