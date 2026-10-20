import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import DashboardPage from './DashboardPage'

const hooks = vi.hoisted(() => ({
  useSystemInfo: vi.fn(),
  useDoctor: vi.fn(),
  useRerunDoctor: vi.fn(),
  useSetupState: vi.fn(),
  useLabels: vi.fn(),
  useNodeLiveness: vi.fn(),
  fetchTraces: vi.fn(),
}))
vi.mock('@/hooks/useSystem', () => ({ useSystemInfo: hooks.useSystemInfo }))
vi.mock('@/hooks/useSetup', () => ({ useDoctor: hooks.useDoctor, useRerunDoctor: hooks.useRerunDoctor, useSetupState: hooks.useSetupState }))
vi.mock('@/hooks/useModelManager', () => ({ useLabels: hooks.useLabels }))
vi.mock('@/hooks/useNodes', () => ({ useNodeLiveness: hooks.useNodeLiveness }))
vi.mock('@/api/traces', () => ({ fetchTraces: hooks.fetchTraces }))
// The banners and the power buttons have their own tests.
vi.mock('@/components/dashboard/ModelBanner', () => ({ default: () => null }))
vi.mock('@/components/dashboard/UpdateBanner', () => ({ default: () => null }))
vi.mock('@/components/dashboard/PowerControls', () => ({ default: () => null }))

/** A react-query result: nothing loaded, nothing failed, unless `over` says so. */
const q = (over: object = {}) => ({ data: undefined, isLoading: false, isError: false, error: null, refetch: vi.fn(), ...over })

const info = {
  hostname: 'box',
  platform: 'linux',
  arch: 'amd64',
  cpuCount: 8,
  totalMemoryMb: 16384,
  version: 'v1',
  uptime: 10,
  listeners: [{ served: true }],
}

const failing = {
  status: 'fail',
  checks: [
    { name: 'listening', status: 'ok' },
    { name: 'firewall 10.0.0.0/24', status: 'fail', detail: 'ufw drops 19700/tcp', fix: 'sudo ufw allow 19700' },
  ],
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  hooks.useSystemInfo.mockReturnValue(q({ data: info }))
  hooks.useDoctor.mockReturnValue(q({ data: failing }))
  hooks.useRerunDoctor.mockReturnValue({ mutate: vi.fn(), isPending: false })
  hooks.useSetupState.mockReturnValue(q({ data: { households: 1, labels: {} } }))
  hooks.useLabels.mockReturnValue(q({ data: { labels: [], voice: [], warnings: [] } }))
  hooks.useNodeLiveness.mockReturnValue(q({ data: [{ node_id: 'k', room: 'kitchen', online: true, last_seen: null }] }))
  hooks.fetchTraces.mockResolvedValue({ traces: [] })
})

const region = (name: string) => screen.getByRole('region', { name })

describe('DashboardPage', () => {
  it('has one h1 and leads with what needs you: the health check before the machine details', () => {
    renderPage()
    expect(screen.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeInTheDocument()
    const health = region('Health check')
    const system = region('System')
    expect(health.compareDocumentPosition(system) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('says what failed and offers to load the server details again', () => {
    const refetch = vi.fn()
    hooks.useSystemInfo.mockReturnValue(q({ isError: true, error: new Error('internal error'), refetch }))
    renderPage()
    const alert = within(region('System')).getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load the server's details")
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(refetch).toHaveBeenCalled()
  })

  it('shows placeholders shaped like the cards while they load', () => {
    hooks.useSystemInfo.mockReturnValue(q({ isLoading: true }))
    hooks.useDoctor.mockReturnValue(q({ isLoading: true }))
    hooks.useNodeLiveness.mockReturnValue(q({ isLoading: true }))
    renderPage()
    expect(within(region('System')).getByRole('status', { name: "Loading the server's details" })).toBeInTheDocument()
    expect(within(region('Health check')).getByRole('status', { name: 'Running the health check' })).toBeInTheDocument()
    expect(within(region('Nodes')).getByRole('status', { name: 'Loading nodes' })).toBeInTheDocument()
  })

  it('counts households in words that agree with the number', () => {
    renderPage()
    expect(within(region('Nodes')).getByText(/1 household$/)).toBeInTheDocument()
  })

  it('names the failing checks and keeps their fixes behind "Show details" on a phone', () => {
    renderPage()
    const health = region('Health check')
    expect(health).toHaveTextContent('1 of 2 checks pass')
    const toggle = within(health).getByRole('button', { name: 'Show details' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(within(health).getByRole('button', { name: 'Hide details' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('offers to run a health check that could not run again', () => {
    const refetch = vi.fn()
    hooks.useDoctor.mockReturnValue(q({ isError: true, error: new Error('boom'), refetch }))
    renderPage()
    const alert = within(region('Health check')).getByRole('alert')
    expect(alert).toHaveTextContent("The health check couldn't run")
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(refetch).toHaveBeenCalled()
  })

  it('says when nothing has asked Jarvis anything yet', async () => {
    renderPage()
    await waitFor(() => expect(within(region('Recent requests')).getByText('No requests yet')).toBeInTheDocument())
  })
})
