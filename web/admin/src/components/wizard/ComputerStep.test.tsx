import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { NodeLiveness } from '@/api/nodes'
import ComputerStep from './ComputerStep'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
const api = vi.hoisted(() => ({ getNodeLiveness: vi.fn(), getPairings: vi.fn(), approvePairing: vi.fn() }))
const chatApi = vi.hoisted(() => ({ getThisDevice: vi.fn() }))
const addonsApi = vi.hoisted(() => ({ fetchAgent: vi.fn(), saveFile: vi.fn() }))
const households = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), ...api }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), ...chatApi }))
vi.mock('@/api/addons', async (orig) => ({ ...(await orig<typeof import('@/api/addons')>()), ...addonsApi }))
vi.mock('@/api/system', () => ({ getSystemInfo: vi.fn().mockResolvedValue({ listeners: [{ name: 'config', port: 7700, served: true }] }) }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), listHouseholds: households.list }))

const agent = (online: boolean, ext?: string): NodeLiveness => ({
  node_id: 'n1',
  room: 'Office-PC',
  household_id: 'h1',
  online,
  last_seen: '2026-10-14T10:00:00',
  last_seen_version: null,
  is_busy: false,
  node_kind: 'desktop',
  capabilities: ext ? { browser_extension: ext } : {},
})

function renderStep() {
  const onNext = vi.fn()
  const onBack = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <ComputerStep onNext={onNext} onBack={onBack} />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
  return { onNext, onBack, qc }
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', 'a.e30.s')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 1, email: 'op@example.com', is_superuser: true }))
  households.list.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'owner' }])
  api.getPairings.mockResolvedValue({ waiting: 0, households: [{ id: 'h1', name: 'Home', role: 'owner' }] })
  api.getNodeLiveness.mockResolvedValue([])
  chatApi.getThisDevice.mockResolvedValue({ node_id: null, reason: 'no_agents' })
  addonsApi.fetchAgent.mockResolvedValue({ blob: new Blob(['x']), filename: 'jarvis-automation-agent_1.2.0_amd64.deb', release: 'v1.2.0' })
})

describe('wizard: This computer (install plan 3.3)', () => {
  it('is skippable and asks nothing until a box is ticked', async () => {
    const { onNext } = renderStep()
    expect(screen.getByRole('heading', { name: /Also use Jarvis Automation on this computer/ })).toBeInTheDocument()
    expect(screen.getByText(/later in/)).toHaveTextContent('You can do this later in Add-ons')
    // The extension needs the agent.
    expect(screen.getByRole('checkbox', { name: /Let Jarvis control this browser/ })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Skip for now' }))
    expect(onNext).toHaveBeenCalledTimes(1)
    expect(api.getNodeLiveness).not.toHaveBeenCalled()
    expect(chatApi.getThisDevice).not.toHaveBeenCalled()
  })

  it('agent only: download, approve the code, and the checks turn green as it connects', async () => {
    api.getPairings.mockResolvedValue({
      waiting: 1,
      households: [{ id: 'h1', name: 'Home', role: 'owner' }],
    })
    const { onNext } = renderStep()
    fireEvent.click(screen.getByRole('checkbox', { name: /^Use Jarvis Automation on this computer/ }))
    const steps = screen.getByRole('list', { name: 'Desktop agent steps' })

    // Download through jarvisd (verified there), saved by the browser.
    fireEvent.click(within(steps).getByRole('button', { name: /^Download/ }))
    await waitFor(() => expect(addonsApi.saveFile).toHaveBeenCalled())
    expect(await within(steps).findByText(/Saved jarvis-automation-agent_1.2.0_amd64.deb/)).toBeInTheDocument()
    expect(within(steps).getByText(/Checked against the signed release v1.2.0/)).toBeInTheDocument()

    // The approval form shows here, to type the code the agent shows.
    expect(await within(steps).findByLabelText('Code shown on the computer')).toBeInTheDocument()
    expect(screen.getByTestId('agent-connected')).toHaveTextContent('Waiting for the agent to connect')

    // It pairs and this-device names it: green.
    api.getNodeLiveness.mockResolvedValue([agent(true)])
    chatApi.getThisDevice.mockResolvedValue({ node_id: 'n1' })
    expect(await screen.findByText('Office-PC', { selector: 'strong' }, { timeout: 5000 })).toBeInTheDocument()
    expect(screen.getByTestId('agent-connected')).toHaveTextContent('The agent is connected (Office-PC)')
    // Approved: the approval form makes way.
    expect(within(steps).queryByLabelText('Code shown on the computer')).toBeNull()
    expect(within(steps).queryAllByLabelText('Not yet')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onNext).toHaveBeenCalled()
  })

  it('agent and extension: the extension tick follows the agent heartbeat', async () => {
    api.getNodeLiveness.mockResolvedValue([agent(true, 'disconnected')])
    // The server can't tell which computer this is (NAT): an online agent of the household counts.
    chatApi.getThisDevice.mockResolvedValue({ node_id: null, reason: 'no_match' })
    renderStep()
    fireEvent.click(screen.getByRole('checkbox', { name: /^Use Jarvis Automation on this computer/ }))
    const ext = screen.getByRole('checkbox', { name: /Let Jarvis control this browser/ })
    expect(ext).toBeEnabled()
    fireEvent.click(ext)
    expect(await screen.findByTestId('extension-state')).toHaveTextContent('Waiting for the extension to connect')
    expect(await screen.findByText('Office-PC', { selector: 'strong' })).toBeInTheDocument()
    // No store listing yet: the guide instead of a dead link.
    expect(screen.getByRole('link', { name: 'How browser control works' })).toBeInTheDocument()

    api.getNodeLiveness.mockResolvedValue([agent(true, 'connected')])
    await waitFor(() => expect(screen.getByTestId('extension-state')).toHaveTextContent('The extension is connected'), { timeout: 5000 })

    // Unticking the agent unticks the extension.
    fireEvent.click(screen.getByRole('checkbox', { name: /^Use Jarvis Automation on this computer/ }))
    expect(screen.getByRole('checkbox', { name: /Let Jarvis control this browser/ })).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Skip for now' })).toBeInTheDocument()
  })

  it('a refused download says why and links the release page', async () => {
    const { AgentDownloadError } = await import('@/api/addons')
    addonsApi.fetchAgent.mockRejectedValue(new AgentDownloadError('SHA256SUMS: signature check failed', 502, 'https://example/rel'))
    renderStep()
    fireEvent.click(screen.getByRole('checkbox', { name: /^Use Jarvis Automation on this computer/ }))
    fireEvent.click(screen.getByRole('button', { name: /^Download/ }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('SHA256SUMS: signature check failed')
    expect(within(alert).getByRole('link', { name: /Release page/ })).toHaveAttribute('href', 'https://example/rel')
    expect(addonsApi.saveFile).not.toHaveBeenCalled()
  })
})
