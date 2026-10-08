import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as api from '@/api/connections'
import { publicURLError, type ConnectionsResponse } from '@/api/connections'
import ConnectionsPage from './ConnectionsPage'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))
vi.mock('@/api/connections', async (importOriginal) => ({
  publicURLError: (await importOriginal<typeof import('@/api/connections')>()).publicURLError,
  getConnections: vi.fn(),
  addService: vi.fn(),
  removeService: vi.fn(),
  createApp: vi.fn(),
  rotateApp: vi.fn(),
  revokeApp: vi.fn(),
  setPublicURL: vi.fn(),
}))

const m = vi.mocked(api)

function response(withHealth: boolean): ConnectionsResponse {
  const h = withHealth ? { healthy: true, latency_ms: 3 } : null
  return {
    listeners: [
      { name: 'jarvis-auth', url: 'http://10.0.0.5:7701', port: 7701, managed: 'listener', public_url: null, health: h },
      { name: 'jarvis-command-center', url: 'http://10.0.0.5:7703', port: 7703, managed: 'listener', public_url: 'https://cc.example.io', health: h },
    ],
    external: [
      { name: 'jarvis-pantry', url: 'https://pantry.example', health_path: '/health', description: '', managed: 'setting', removable: false, public_url: null, health: h },
      { name: 'jarvis-recipes', url: 'http://10.0.0.9:7030', health_path: '/health', description: 'recipes', managed: '', removable: true, public_url: null, health: h },
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

  it('shows public URLs and edits one in place, with validation and the tunnel hint', async () => {
    m.setPublicURL.mockResolvedValue({ name: 'jarvis-auth', url: 'http://10.0.0.5:7701', public_url: 'https://auth.example.io' })
    renderPage()
    expect(await screen.findByTestId('public-url-jarvis-command-center')).toHaveTextContent('https://cc.example.io')
    const row = screen.getByTestId('listener-jarvis-auth')
    expect(within(row).getByText('no public URL')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Edit the public URL of jarvis-auth' }))
    const input = within(row).getByLabelText('Public URL for jarvis-auth')
    expect(within(row).getByText(/Cloudflare tunnel/)).toBeInTheDocument()

    fireEvent.change(input, { target: { value: 'https://auth.example.io/api' } })
    expect(within(row).getByRole('alert')).toHaveTextContent(/no path/)
    expect(within(row).getByRole('button', { name: 'Save' })).toBeDisabled()

    fireEvent.change(input, { target: { value: 'https://auth.example.io' } })
    expect(within(row).queryByRole('alert')).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(m.setPublicURL).toHaveBeenCalledWith('jarvis-auth', 'https://auth.example.io'))
  })

  it('clears a public URL by saving it empty', async () => {
    m.setPublicURL.mockResolvedValue({ name: 'jarvis-command-center', url: 'http://10.0.0.5:7703', public_url: null })
    renderPage()
    const row = await screen.findByTestId('listener-jarvis-command-center')
    fireEvent.click(within(row).getByRole('button', { name: /Edit the public URL/ }))
    fireEvent.change(within(row).getByLabelText(/Public URL for/), { target: { value: '' } })
    fireEvent.click(within(row).getByRole('button', { name: 'Clear' }))
    await waitFor(() => expect(m.setPublicURL).toHaveBeenCalledWith('jarvis-command-center', null))
  })

  it('sends an optional public URL with a new external entry', async () => {
    m.addService.mockResolvedValue({ name: 'jarvis-recipes-server', url: 'http://10.0.0.9:7030', health_path: '/health', description: '', managed: '', removable: true, public_url: 'https://recipes.example.io', health: null })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Add/ }))
    fireEvent.change(screen.getByLabelText('Service name'), { target: { value: 'jarvis-recipes-server' } })
    fireEvent.change(screen.getByLabelText('Base URL'), { target: { value: 'http://10.0.0.9:7030' } })
    fireEvent.change(screen.getByLabelText('Public URL'), { target: { value: 'https://recipes.example.io' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(m.addService).toHaveBeenCalledWith(expect.objectContaining({ public_url: 'https://recipes.example.io' })))
  })
})

describe('publicURLError', () => {
  it.each([
    ['', null],
    ['https://command-center.example.io', null],
    ['wss://mqtt.example.io', null],
    ['https://cc.example.io:8443/', null],
    ['http://[2001:db8::1]:80', null],
    ['command-center.example.io', 'Start with a scheme, e.g. https://'],
    ['ftp://x.io', 'Use http, https, ws, wss, mqtt or mqtts'],
    ['https://x.io/path', 'Base URL only: scheme, host and optional port, no path'],
    ['https://x.io?q=1', 'Base URL only: scheme, host and optional port, no path'],
    ['https://u:p@x.io', 'No user name or password in the URL'],
    ['https://x.io:70000', 'Port must be 1–65535'],
    ['https://localhost', "A public URL can't be this machine's loopback address"],
  ])('%s', (raw, want) => {
    expect(publicURLError(raw)).toBe(want)
  })
})
