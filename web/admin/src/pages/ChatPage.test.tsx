import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { ChatEvent, ChatTurnRequest } from '@/api/chat'
import type { NodeLiveness } from '@/api/nodes'
import ChatPage from './ChatPage'

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

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

const node = (node_id: string, online: boolean, room = node_id): NodeLiveness => ({
  node_id,
  room,
  household_id: 'h1',
  online,
  last_seen: null,
  last_seen_version: null,
  is_busy: false,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <ChatPage />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

/** streamChat stand-in: plays the given events, then resolves. */
function plays(events: ChatEvent[]) {
  api.streamChat.mockImplementation(async (_req: ChatTurnRequest, onEvent: (e: ChatEvent) => void) => {
    for (const e of events) onEvent(e)
  })
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', jwt('h1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', username: 'emm', display_name: 'Emma', is_superuser: false }))
  api.listChatNodes.mockResolvedValue([node('kitchen-pi', true, 'kitchen'), node('primary', true, 'office')])
  api.getPrimaryNodeId.mockResolvedValue('primary')
  api.getThisDevice.mockResolvedValue({ node_id: null, reason: 'no_match' })
  api.getChatCapabilities.mockResolvedValue({ images: false, max_images: 4, max_image_bytes: 2 << 20 })
  api.getNodeTools.mockResolvedValue({ client_tools: [{ type: 'function', function: { name: 'get_weather' } }], available_commands: [] })
  api.warmupChat.mockResolvedValue({ conversation_id: 'warm-1' })
})

const picker = () => screen.getByLabelText('Device') as HTMLSelectElement

describe('ChatPage', () => {
  it('picks the primary node, lists the household with ?household_id= and warms up with its tools', async () => {
    renderPage()
    await waitFor(() => expect(picker().value).toBe('primary'))
    expect(api.listChatNodes).toHaveBeenCalledWith('h1')
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    expect(api.warmupChat.mock.calls[0][0]).toMatchObject({
      node_id: 'primary',
      household_id: 'h1',
      client_tools: [{ type: 'function', function: { name: 'get_weather' } }],
    })
    expect(screen.queryByRole('status')).toBeNull()
  })

  it("picks this computer's agent first and labels it", async () => {
    api.listChatNodes.mockResolvedValue([node('kitchen-pi', true, 'kitchen'), node('primary', true, 'office'), { ...node('laptop', true, 'desk'), node_kind: 'desktop' }])
    api.getThisDevice.mockResolvedValue({ node_id: 'laptop', household_id: 'h1', online: true })
    renderPage()
    await waitFor(() => expect(picker().value).toBe('laptop'))
    expect(api.getThisDevice).toHaveBeenCalledWith('h1')
    expect(screen.getByRole('option', { name: 'desk (this computer)' })).toBeInTheDocument()

    // Chatting to this computer's own agent: the turn says so, for the same-device hint.
    plays([{ type: 'done', full_text: 'Typed.', conversation_id: 'c-9' }])
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'type hello' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    expect(await screen.findByText('Typed.')).toBeInTheDocument()
    expect((api.streamChat.mock.calls[0][0] as ChatTurnRequest).client).toEqual({ surface: 'web', same_device: true })
  })

  it("falls back to the primary when the server can't tell or fails", async () => {
    api.getThisDevice.mockRejectedValue(new Error('boom'))
    renderPage()
    await waitFor(() => expect(picker().value).toBe('primary'))
  })

  it('shows the offline banner and keeps the input enabled', async () => {
    api.listChatNodes.mockResolvedValue([node('office-pc', false, 'office')])
    api.getPrimaryNodeId.mockResolvedValue('')
    renderPage()
    expect(await screen.findByText(/Office is offline: questions work, commands that run on it won't/)).toBeInTheDocument()
    expect(screen.getByLabelText('Message')).toBeEnabled()
  })

  it('offers photos only when the live model takes images', async () => {
    renderPage()
    await waitFor(() => expect(picker().value).toBe('primary'))
    expect(screen.queryByRole('button', { name: 'Add a photo' })).toBeNull()
  })

  it('offers photos when capabilities say images', async () => {
    api.getChatCapabilities.mockResolvedValue({ images: true, max_images: 4, max_image_bytes: 2 << 20 })
    renderPage()
    expect(await screen.findByRole('button', { name: 'Add a photo' })).toBeInTheDocument()
  })

  it('streams a turn, renders markdown without raw HTML, and continues the conversation', async () => {
    plays([
      { type: 'status', message: 'Starting conversation...' },
      { type: 'delta', text: '**Sunny** ' },
      { type: 'done', full_text: '**Sunny** and <img src=x onerror=alert(1)>warm', conversation_id: 'c-1', reasoning: 'checked the forecast' },
    ])
    renderPage()
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'weather?' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    expect(await screen.findByText('Sunny')).toBeInTheDocument()
    expect(screen.getByText('Sunny').tagName).toBe('STRONG')
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.getByText('Emma')).toBeInTheDocument()
    const first = api.streamChat.mock.calls[0][0] as ChatTurnRequest
    expect(first).toMatchObject({ message: 'weather?', node_id: 'primary', household_id: 'h1', conversation_id: 'warm-1', include_reasoning: true })
    expect(first.client).toEqual({ surface: 'web', same_device: false })
    // The tools went with the warmup; a warm conversation doesn't resend them.
    expect(first.client_tools).toBeUndefined()

    plays([{ type: 'done', full_text: 'Still sunny.', conversation_id: 'c-1' }])
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'and later?' } })
    await act(async () => {
      fireEvent.keyDown(screen.getByLabelText('Message'), { key: 'Enter' })
    })
    expect(await screen.findByText('Still sunny.')).toBeInTheDocument()
    expect((api.streamChat.mock.calls[1][0] as ChatTurnRequest).conversation_id).toBe('c-1')
    // The conversation is this tab's: it survives a reload.
    expect(sessionStorage.getItem('jarvis-admin:chat:7:h1')).toContain('Still sunny.')
  })

  it('runs an action button on the node and answers a validation option as the next message', async () => {
    plays([
      {
        type: 'done',
        full_text: 'Type "hello" into Notes?',
        conversation_id: 'c-2',
        actions: [{ button_text: 'Type it', button_action: 'confirm', button_type: 'primary', completion_message: 'Typed.' }],
        action_context: { command_name: 'desktop_type_text', context: { text: 'hello' } },
      },
    ])
    api.sendNodeAction.mockResolvedValue({ status: 'completed', success: true })
    renderPage()
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'type hello' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    await act(async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Type it' }))
    })
    expect(api.sendNodeAction).toHaveBeenCalledWith('primary', {
      command_name: 'desktop_type_text',
      action_name: 'confirm',
      context: { text: 'hello' },
    })
    expect(await screen.findByText('Typed.')).toBeInTheDocument()

    plays([
      {
        type: 'done',
        full_text: 'Which room?',
        stop_reason: 'validation',
        validation: { question: 'Which room?', parameter_name: 'room', options: ['Kitchen', { label: 'Office' }] },
      },
    ])
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'lights on' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    plays([{ type: 'done', full_text: 'Done.' }])
    await act(async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Office' }))
    })
    expect((api.streamChat.mock.calls[2][0] as ChatTurnRequest).message).toBe('Office')
  })

  it('starts a new conversation when another node is picked, and remembers the pick', async () => {
    plays([{ type: 'done', full_text: 'Hi there.', conversation_id: 'c-3' }])
    renderPage()
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hi' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    })
    expect(await screen.findByText('Hi there.')).toBeInTheDocument()
    await act(async () => {
      fireEvent.change(picker(), { target: { value: 'kitchen-pi' } })
    })
    expect(screen.queryByText('Hi there.')).toBeNull()
    expect(localStorage.getItem('jarvis-admin:u:chat-node:h1')).toBe('kitchen-pi')
    await waitFor(() => expect(api.warmupChat.mock.calls.at(-1)?.[0].node_id).toBe('kitchen-pi'))
  })
  it("asks in the house's words, names both sides, and shows an empty Send as off", async () => {
    plays([{ type: 'done', full_text: 'Hello.', conversation_id: 'c-4' }])
    renderPage()
    await waitFor(() => expect(api.warmupChat).toHaveBeenCalled())
    const input = screen.getByLabelText('Message')
    expect(input).toHaveAttribute('placeholder', 'Ask Jarvis')
    const send = screen.getByRole('button', { name: 'Send' })
    expect(send).toBeDisabled()
    expect(send).not.toHaveClass('bg-primary')
    fireEvent.change(input, { target: { value: 'hi' } })
    expect(send).toHaveClass('bg-primary')
    await act(async () => {
      fireEvent.click(send)
    })
    expect(await screen.findByText('Hello.')).toBeInTheDocument()
    // Each side's name is just above what it said, outside the message itself (e2e reads its exact text).
    const answer = document.querySelector('[data-role="assistant"]') as HTMLElement
    expect(answer.previousElementSibling).toHaveTextContent(/^Jarvis$/)
    expect(answer).toHaveTextContent(/^Hello\.$/)
    expect(document.querySelector('[data-role="user"]')?.previousElementSibling).toHaveTextContent(/^Emma$/)
  })
})
