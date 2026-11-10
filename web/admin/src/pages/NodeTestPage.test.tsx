import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { AuthProvider } from '@/auth/AuthContext'
import { apiClient } from '@/api/client'
import type { ChatEvent, ChatTurnRequest } from '@/api/chat'
import NodeTestPage from './NodeTestPage'

const api = vi.hoisted(() => ({
  listChatNodes: vi.fn(),
  getPrimaryNodeId: vi.fn(),
  getThisDevice: vi.fn(),
  getChatCapabilities: vi.fn(),
  getNodeTools: vi.fn(),
  warmupChat: vi.fn(),
  streamChat: vi.fn(),
  sendNodeAction: vi.fn(),
}))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), ...api }))
vi.mock('@/api/auth', async (orig) => ({
  ...(await orig<typeof import('@/api/auth')>()),
  listHouseholds: vi.fn().mockResolvedValue([]),
}))

const get = vi.spyOn(apiClient, 'get')
const post = vi.spyOn(apiClient, 'post')

const TI = '/api/cc/api/v0/nodes/pc1/test-install'
const live = {
  node_id: 'pc1',
  room: 'desktop',
  name: 'office-pc.local',
  household_id: 'hh1',
  online: true,
  last_seen: null,
  last_seen_version: null,
  is_busy: false,
  node_kind: 'desktop',
}

let poll: Record<string, unknown>
function Search() {
  return <output data-testid="search">{useLocation().search}</output>
}
const search = () => screen.getByTestId('search').textContent

function renderPage(path = '/admin/nodes/pc1/test') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route
              path="/admin/nodes/:id/test"
              element={
                <>
                  <NodeTestPage />
                  <Search />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

const completed = {
  status: 'completed',
  request_id: 'r1',
  package_name: 'coin_flip',
  error_message: null,
  details: { commands: ['flip_coin'] },
  runs: [{ command: 'flip_coin', arguments: '{}', output: { success: false, error: 'KeyError: sides' }, at: new Date().toISOString() }],
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  localStorage.setItem('jarvis-admin:access_token', jwt('hh1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'su@example.com', username: 'su', is_superuser: true }))
  poll = { ...completed }
  get.mockImplementation(async (url: string) => {
    if (url === '/api/cc/api/v0/admin/nodes') return { data: [live] }
    if (url === '/api/admin/households') return { data: [{ id: 'hh1', name: 'Home' }] }
    if (url === '/api/admin/nodes') return { data: [{ node_id: 'pc1', name: 'Office-PC', household_id: 'hh1', is_active: true, services: [] }] }
    if (url === `${TI}/r1`) return { data: poll }
    throw new Error(`unexpected GET ${url}`)
  })
  post.mockImplementation(async (url: string) => {
    if (url === TI) return { data: { id: 'r1', status: 'pending', package_name: 'coin_flip' } }
    if (url === `${TI}/r1/feedback`) return { data: { status: 'sent', runs: 1 } }
    throw new Error(`unexpected POST ${url}`)
  })
  api.listChatNodes.mockResolvedValue([live])
  api.getPrimaryNodeId.mockResolvedValue('other')
  api.getThisDevice.mockResolvedValue({ node_id: null })
  api.getChatCapabilities.mockResolvedValue({ images: false, max_images: 4, max_image_bytes: 2 << 20 })
  api.getNodeTools.mockResolvedValue({ client_tools: [], available_commands: [{ command_name: 'flip_coin' }] })
  api.warmupChat.mockResolvedValue({ conversation_id: 'warm-1' })
})

describe('NodeTestPage', () => {
  it('takes a share code, starts the test install on this node and keeps the request in the URL', async () => {
    poll = { ...completed, status: 'pending', details: null, runs: [] }
    renderPage()
    const code = await screen.findByLabelText('Share code')
    const install = await screen.findByRole('button', { name: 'Install on Office-PC' })
    fireEvent.change(code, { target: { value: 'ab3-km' } })
    expect(code).toHaveValue('AB3KM')
    expect(install).toBeDisabled()
    fireEvent.change(code, { target: { value: 'ab3km7x' } })
    expect(code).toHaveValue('AB3KM7')
    fireEvent.click(install)
    await waitFor(() => expect(post).toHaveBeenCalledWith(TI, { share_code: 'AB3KM7' }))
    expect(await screen.findByText('Installing')).toBeInTheDocument()
    expect(search()).toBe('?request=r1&code=AB3KM7')
  })

  it("doesn't offer the install while the node is offline", async () => {
    get.mockImplementation(async (url: string) => {
      if (url === '/api/cc/api/v0/admin/nodes') return { data: [{ ...live, online: false }] }
      if (url === '/api/admin/households') return { data: [] }
      if (url === '/api/admin/nodes') return { data: [{ node_id: 'pc1', name: 'Office-PC', household_id: 'hh1', is_active: true, services: [] }] }
      throw new Error(`unexpected GET ${url}`)
    })
    renderPage()
    expect(await screen.findByText('Office-PC is offline. Test installs need it online.')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Share code'), { target: { value: 'AB3KM7' } })
    expect(screen.getByRole('button', { name: 'Install on Office-PC' })).toBeDisabled()
  })

  it('shows why the server refused the install', async () => {
    post.mockRejectedValue({ isAxiosError: true, response: { status: 403, data: { detail: 'Pantry is turned off for this household' } } })
    renderPage()
    fireEvent.change(await screen.findByLabelText('Share code'), { target: { value: 'AB3KM7' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Install on Office-PC' }))
    expect(await screen.findByText('Pantry is turned off for this household')).toBeInTheDocument()
  })

  it('shows a failed install and starts over', async () => {
    poll = { ...completed, status: 'failed', error_message: 'No module named requests', details: null, runs: [] }
    renderPage('/admin/nodes/pc1/test?request=r1')
    expect(await screen.findByText('No module named requests')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Try another code' }))
    expect(await screen.findByLabelText('Share code')).toBeInTheDocument()
    expect(search()).toBe('')
  })

  it('reinstalls the same code after a fix, as a new test with a fresh chat', async () => {
    post.mockImplementation(async (url: string) => {
      if (url === TI) return { data: { id: 'r2', status: 'pending', package_name: 'coin_flip' } }
      throw new Error(`unexpected POST ${url}`)
    })
    get.mockImplementation(async (url: string) => {
      if (url === '/api/cc/api/v0/admin/nodes') return { data: [live] }
      if (url === '/api/admin/households') return { data: [] }
      if (url === '/api/admin/nodes') return { data: [] }
      if (url === `${TI}/r1` || url === `${TI}/r2`) return { data: poll }
      throw new Error(`unexpected GET ${url}`)
    })
    renderPage('/admin/nodes/pc1/test?request=r1&code=AB3KM7')
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalledTimes(1))
    fireEvent.click(await screen.findByRole('button', { name: 'Reinstall' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith(TI, { share_code: 'AB3KM7' }))
    await waitFor(() => expect(search()).toBe('?request=r2&code=AB3KM7'))
    // The chat starts again, so its tools are the new version's.
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalledTimes(2))
  })

  it('offers no Reinstall without the code', async () => {
    renderPage('/admin/nodes/pc1/test?request=r1')
    expect(await screen.findByRole('button', { name: 'Try another code' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reinstall' })).toBeNull()
  })

  it('shows the node events Send to Forge will include', async () => {
    poll = {
      ...completed,
      events: [
        { kind: 'log', level: 'WARNING', message: 'Zigbee2MQTT broker host is not configured' },
        { kind: 'scan', protocol: 'zigbee', count: 0 },
        { kind: 'control', action: 'turn_on', entity_id: 'zigbee_0xa4', success: false, error: 'timeout' },
      ],
    }
    renderPage('/admin/nodes/pc1/test?request=r1')
    expect(await screen.findByText('WARNING Zigbee2MQTT broker host is not configured')).toBeInTheDocument()
    expect(screen.getByText('Scan (zigbee): 0 found')).toBeInTheDocument()
    expect(screen.getByText('turn_on zigbee_0xa4: timeout')).toBeInTheDocument()
  })

  it('says a device protocol loaded and where its settings go', async () => {
    poll = { ...completed, package_name: 'zigbee2mqtt', details: { commands: [], protocols: ['zigbee2mqtt'] }, runs: [] }
    renderPage('/admin/nodes/pc1/test?request=r1')
    expect(await screen.findByText('zigbee2mqtt', { selector: 'span' })).toBeInTheDocument()
    expect(screen.getByText(/Set its settings in the mobile app/)).toBeInTheDocument()
    expect(screen.getByText(/Once its devices are added, control them here/)).toBeInTheDocument()
  })

  it('lists the runs and sends them to the Forge with the note', async () => {
    renderPage('/admin/nodes/pc1/test?request=r1')
    expect(await screen.findByText('KeyError: sides')).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.getByText('flip_coin', { selector: 'span' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Note (optional)'), { target: { value: '  it should default to two sides ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send to Forge' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith(`${TI}/r1/feedback`, { note: 'it should default to two sides' }))
    expect(await screen.findByText('Sent. On the Forge page, use Fix with this report, then Reinstall here.')).toBeInTheDocument()
  })

  it("chats through the test node only, with fresh tools, and leaves the chat page's conversation alone", async () => {
    const saved = 'jarvis-admin:chat:7:hh1'
    sessionStorage.setItem(saved, JSON.stringify({ nodeId: 'other', conversationId: 'c-main', messages: [] }))
    api.streamChat.mockImplementation(async (_req: ChatTurnRequest, onEvent: (e: ChatEvent) => void) => {
      onEvent({ type: 'done', full_text: 'It landed on heads.', conversation_id: 'c-test' })
    })
    renderPage('/admin/nodes/pc1/test?request=r1')
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    expect(api.getNodeTools).toHaveBeenCalledWith('pc1')
    expect(api.warmupChat.mock.calls[0][0]).toMatchObject({ node_id: 'pc1', household_id: 'hh1', available_commands: [{ command_name: 'flip_coin' }] })

    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'flip a coin' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    expect(await screen.findByText('It landed on heads.')).toBeInTheDocument()
    expect((api.streamChat.mock.calls[0][0] as ChatTurnRequest).node_id).toBe('pc1')
    expect(JSON.parse(sessionStorage.getItem(saved)!)).toMatchObject({ nodeId: 'other', conversationId: 'c-main' })
  })
})
