import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { toast } from 'sonner'
import { apiClient } from '@/api/client'
import type { SipAccount, SipStatus, SipTraceDialog } from '@/api/phone'
import PhonePage from './PhonePage'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), loading: vi.fn() }),
}))

function account(over: Partial<SipAccount> = {}): SipAccount {
  return {
    local_port: 5062,
    server: 'pbx.lan',
    port: 5060,
    transport: 'udp',
    extension: '100',
    dial_format: '10-digit',
    dial_prefix: '',
    household_id: '',
    state: 'registered',
    since: '2026-10-22T08:00:00Z',
    last_error: '',
    hint: '',
    auth_failures: 0,
    attempts: 1,
    register_rtt_ms: 31,
    options_rtt_ms: 4,
    active_calls: 0,
    tests: {},
    ...over,
  }
}

let status: SipStatus
let trace: SipTraceDialog[]
const get = vi.spyOn(apiClient, 'get')
const post = vi.spyOn(apiClient, 'post')

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <PhonePage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  status = { accounts: [account()], tests: ['connection', 'echo', 'digits', 'call_me', 'inbound'], feature_codes: ['*43', '*65'] }
  trace = []
  get.mockImplementation(async (url: string) => {
    if (url === '/api/phone/sip') return { data: status }
    if (url === '/api/phone/sip/trace') return { data: { dialogs: trace } }
    throw new Error('unexpected GET ' + url)
  })
  post.mockResolvedValue({ data: { test: 'connection', state: 'running', started: '2026-10-22T08:00:00Z', summary: '', steps: [] } })
})

describe('PhonePage', () => {
  it('shows the empty state with a way to Settings when no SIP account runs', async () => {
    status.accounts = []
    renderPage()
    expect(await screen.findByRole('heading', { name: 'No SIP account is running' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Settings' })).toHaveAttribute('href', '/admin/settings')
  })

  it('shows an account: registration, round trips and the five self-tests', async () => {
    renderPage()
    const card = await screen.findByRole('region', { name: 'Extension 100 on pbx.lan:5060' })
    expect(within(card).getByText('Registered')).toBeInTheDocument()
    expect(within(card).getByText('4 ms')).toBeInTheDocument()
    expect(within(card).getByText('31 ms')).toBeInTheDocument()
    expect(within(card).getByText(/System account · UDP · local port 5062/)).toBeInTheDocument()
    for (const name of ['Connection', 'Echo test (*43)', 'Hear the PBX (*65)', 'Call my phone', 'Inbound call']) {
      expect(within(card).getByRole('heading', { name })).toBeInTheDocument()
    }
  })

  it('a stopped registration says what went wrong and what to do', async () => {
    status.accounts = [account({ state: 'stopped', last_error: 'REGISTER: 403 Forbidden', hint: 'unblock it in the PBX firewall' })]
    renderPage()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('REGISTER: 403 Forbidden')
    expect(alert).toHaveTextContent('unblock it in the PBX firewall')
    expect(screen.getByText('Stopped')).toBeInTheDocument()
  })

  it('runs a self-test on its account', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run: Echo test (*43)' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/phone/sip/5062/tests/echo', {}))
  })

  it('call my phone needs a number and sends it', async () => {
    renderPage()
    const run = await screen.findByRole('button', { name: 'Run: Call my phone' })
    expect(run).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Your phone number'), { target: { value: '(212) 555-0142' } })
    fireEvent.click(run)
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/phone/sip/5062/tests/call_me', { number: '(212) 555-0142' }))
  })

  it("shows a run's result and blocks other tests while one runs", async () => {
    status.accounts = [
      account({
        tests: {
          echo: {
            test: 'echo',
            state: 'passed',
            started: '2026-10-22T08:00:00Z',
            finished: '2026-10-22T08:00:40Z',
            summary: 'Both audio directions work.',
            steps: ['Dialing *43'],
            latency_ms: 140,
            wer: 0,
            expected: 'Jarvis is checking',
            heard: 'Jarvis is checking',
          },
          inbound: { test: 'inbound', state: 'running', started: '2026-10-22T08:01:00Z', summary: '', steps: ['Call this extension now'] },
        },
      }),
    ]
    renderPage()
    const result = await screen.findByTestId('run-echo')
    expect(result).toHaveTextContent('Passed')
    expect(result).toHaveTextContent('140 ms')
    expect(result).toHaveTextContent('0%')
    expect(screen.getByTestId('run-inbound')).toHaveTextContent('Call this extension now…')
    expect(screen.getByRole('button', { name: 'Run again: Echo test (*43)' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Run: Connection' })).toBeDisabled()
  })

  it('a refused start is a toast', async () => {
    post.mockRejectedValueOnce(Object.assign(new Error('409'), { isAxiosError: true, response: { status: 409, data: { detail: 'a self-test is already running on this account' } } }))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run: Connection' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
  })

  it('lists recent traced calls and exports the trace as a file', async () => {
    trace = [
      {
        call_id: 'c1',
        account: '100@pbx.lan:5060',
        kind: 'call',
        method: 'INVITE',
        started: '2026-10-22T08:00:00Z',
        updated: '2026-10-22T08:00:05Z',
        dropped: 0,
        messages: [
          { at: '2026-10-22T08:00:00Z', dir: 'sent', transport: 'UDP', local: 'a', remote: 'b', first_line: 'INVITE sip:*43@pbx.lan SIP/2.0', text: '' },
        ],
      },
    ]
    const createURL = vi.fn(() => 'blob:x')
    Object.assign(URL, { createObjectURL: createURL, revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderPage()
    const card = await screen.findByRole('region', { name: 'SIP trace' })
    expect(await within(card).findByText('INVITE sip:*43@pbx.lan SIP/2.0')).toBeInTheDocument()
    get.mockImplementationOnce(async () => ({ data: new Blob(['Jarvis SIP trace']), headers: { 'content-disposition': 'attachment; filename="t.txt"' } }))
    fireEvent.click(within(card).getByRole('button', { name: 'Export' }))
    await waitFor(() => expect(createURL).toHaveBeenCalled())
    expect(get).toHaveBeenCalledWith('/api/phone/sip/trace', { params: { format: 'text' }, responseType: 'blob' })
    expect(click).toHaveBeenCalled()
  })
})
