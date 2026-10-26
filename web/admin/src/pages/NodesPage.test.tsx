import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { apiClient } from '@/api/client'
import NodesPage from './NodesPage'

const get = vi.spyOn(apiClient, 'get')
const post = vi.spyOn(apiClient, 'post')

beforeEach(() => {
  vi.clearAllMocks()
  get.mockImplementation(async (url: string) => {
    if (url === '/api/admin/households') return { data: [{ id: 'hh1', name: 'Home' }] }
    if (url === '/api/admin/nodes')
      return {
        data: [
          { node_id: 'pc1', name: 'Office-PC', household_id: 'hh1', is_active: true, services: [] },
          { node_id: 'pi1', name: 'Kitchen', household_id: 'hh1', is_active: true, services: [] },
          { node_id: 'old1', name: 'Old-PC', household_id: 'hh1', is_active: false, services: [] },
        ],
      }
    if (url === '/api/cc/api/v0/admin/nodes')
      return {
        data: [
          { node_id: 'pc1', room: 'office', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false, node_kind: 'desktop', capabilities: { browser_extension: 'connected', computer_control: 'on' } },
          { node_id: 'pi1', room: 'kitchen', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false, node_kind: null, capabilities: { led_mode: 'responding' } },
        ],
      }
    if (url === '/api/cc/api/v0/nodes/pairings') return { data: { waiting: 0, households: [] } }
    throw new Error(`unexpected GET ${url}`)
  })
})

describe('NodesPage', () => {
  it('shows a desktop agent as a computer with its browser extension state', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('connected')).toBeInTheDocument()
    expect(screen.getByLabelText('Computer')).toBeInTheDocument()
    // The Pi reports no extension row.
    expect(screen.getAllByText(/Browser extension:/)).toHaveLength(1)
    // Computer control is shown, read-only, for the computer only.
    expect(screen.getAllByText(/Computer control:/)).toHaveLength(1)
    expect(screen.getByText('Computer control:').parentElement).toHaveTextContent('Computer control: on')
  })

  it('shows computer control off for a computer that never reported it', async () => {
    get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/households') return { data: [{ id: 'hh1', name: 'Home' }] }
      if (url === '/api/admin/nodes') return { data: [{ node_id: 'pc1', name: 'Office-PC', household_id: 'hh1', is_active: true, services: [] }] }
      if (url === '/api/cc/api/v0/admin/nodes')
        return { data: [{ node_id: 'pc1', room: 'office', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false, node_kind: 'desktop', capabilities: {} }] }
      if (url === '/api/cc/api/v0/nodes/pairings') return { data: { waiting: 0, households: [] } }
      throw new Error(`unexpected GET ${url}`)
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Computer control:')).toBeInTheDocument()
    expect(screen.getByText('Computer control:').parentElement).toHaveTextContent('Computer control: off')
  })

  it('says when a connected extension has website access off', async () => {
    get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/households') return { data: [{ id: 'hh1', name: 'Home' }] }
      if (url === '/api/admin/nodes')
        return {
          data: [
            { node_id: 'pc1', name: 'Office-PC', household_id: 'hh1', is_active: true, services: [] },
            { node_id: 'pc2', name: 'Den-PC', household_id: 'hh1', is_active: true, services: [] },
          ],
        }
      if (url === '/api/cc/api/v0/admin/nodes')
        return {
          data: [
            { node_id: 'pc1', room: 'office', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false, node_kind: 'desktop', capabilities: { browser_extension: 'connected', browser_website_access: 'off' } },
            { node_id: 'pc2', room: 'den', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false, node_kind: 'desktop', capabilities: { browser_extension: 'connected', browser_website_access: 'on' } },
          ],
        }
      if (url === '/api/cc/api/v0/nodes/pairings') return { data: { waiting: 0, households: [] } }
      throw new Error(`unexpected GET ${url}`)
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(await screen.findByTestId('node-pc1')).toHaveTextContent('Browser extension: connected, website access off')
    // On is the normal case: just "connected".
    expect(screen.getByTestId('node-pc2')).toHaveTextContent('Browser extension: connected')
    expect(screen.getByTestId('node-pc2')).not.toHaveTextContent('website access')
  })

  it('hides signed-out registrations until asked', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Office-PC')).toBeInTheDocument()
    expect(screen.queryByText('Old-PC')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show 1 signed-out node' }))
    expect(screen.getByText('Old-PC')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Hide signed-out nodes' }))
    expect(screen.queryByText('Old-PC')).not.toBeInTheDocument()
  })

  it('shows a placeholder list while loading, then the nodes', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(screen.getByRole('status', { name: 'Loading nodes' })).toBeInTheDocument()
    expect(await screen.findByText('Office-PC')).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: 'Loading nodes' })).toBeNull()
  })

  it('says what failed and loads again on Try again', async () => {
    let fail = true
    const base = get.getMockImplementation()!
    get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/nodes' && fail) throw new Error('connection refused')
      return base(url)
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load the nodes")
    fail = false
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('Office-PC')).toBeInTheDocument()
  })

  it('says how to add a node when there are none', async () => {
    const base = get.getMockImplementation()!
    get.mockImplementation(async (url: string) => {
      if (url === '/api/admin/households' || url === '/api/admin/nodes') return { data: [] }
      return base(url)
    })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('No nodes yet')).toBeInTheDocument()
    expect(screen.getByText(/Jarvis Automation phone app/)).toBeInTheDocument()
  })

  it('shows a voice node its LED mode and sends a change to that node alone', async () => {
    post.mockResolvedValue({ data: { status: 'sent' } })
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <NodesPage />
      </QueryClientProvider>,
    )
    const leds = await screen.findByLabelText('LEDs')
    // Only the Pi reports an LED mode; the computer has no such row.
    expect(screen.getAllByLabelText('LEDs')).toHaveLength(1)
    expect(leds).toHaveValue('responding')
    fireEvent.change(leds, { target: { value: 'always' } })
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pi1/node-config', {
        settings: { led_enabled: true, led_idle_enabled: true },
        restart: false,
      }),
    )
    fireEvent.change(leds, { target: { value: 'off' } })
    await waitFor(() =>
      expect(post).toHaveBeenLastCalledWith('/api/cc/api/v0/nodes/pi1/node-config', {
        settings: { led_enabled: false },
        restart: false,
      }),
    )
  })
})
