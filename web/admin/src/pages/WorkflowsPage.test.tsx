import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { TaughtWorkflow } from '@/api/workflows'
import WorkflowsPage from './WorkflowsPage'

const api = vi.hoisted(() => ({ listWorkflows: vi.fn(), runWorkflow: vi.fn() }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/api/workflows', async (orig) => ({ ...(await orig<typeof import('@/api/workflows')>()), ...api }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), listHouseholds: vi.fn().mockResolvedValue([]) }))
vi.mock('sonner', () => ({ toast }))

const wf = (over: Partial<TaughtWorkflow>): TaughtWorkflow => ({
  id: 'tw_1',
  household_id: 'h1',
  owner_id: 7,
  name: 'Download bank statement',
  slug: 's',
  triggers: [],
  params: [],
  steps: [{ intent: 'Open the bank', target: 'browser', action: { kind: 'navigate', intent: 'Open the bank', url: 'https://bank.example.com' } }],
  success: {},
  shared: false,
  version: 1,
  created_at: '',
  updated_at: '',
  editable: true,
  ...over,
})

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', jwt('h1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', is_superuser: false }))
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <WorkflowsPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

describe('WorkflowsPage', () => {
  it("lists the household's workflows with owner, sharing and a link to each", async () => {
    api.listWorkflows.mockResolvedValue([wf({}), wf({ id: 'tw_2', name: 'Pay the water bill', owner_id: 9, shared: true, editable: false })])
    renderPage()
    const mine = await screen.findByRole('link', { name: /Download bank statement/ })
    expect(api.listWorkflows).toHaveBeenCalledWith('h1')
    expect(mine).toHaveAttribute('href', '/workflows/tw_1')
    expect(mine).toHaveTextContent('Yours')
    expect(mine).toHaveTextContent('1 step')
    expect(mine).toHaveTextContent('Last run: never')
    const theirs = screen.getByRole('link', { name: /Pay the water bill/ })
    expect(theirs).toHaveTextContent('Another member’s')
    expect(theirs).toHaveTextContent('Shared with the household')
  })

  it("says so when running from here isn't available yet", async () => {
    api.listWorkflows.mockResolvedValue([wf({})])
    const res = { status: 501, data: { detail: 'Replaying a workflow is not available yet' }, statusText: '', headers: {}, config: { headers: new AxiosHeaders() } }
    api.runWorkflow.mockRejectedValue(new AxiosError('501', 'ERR', undefined, undefined, res))
    renderPage()
    await act(async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Run Download bank statement' }))
    })
    expect(api.runWorkflow.mock.calls[0][0]).toBe('tw_1')
    expect(toast.error).toHaveBeenCalledWith("Running a workflow from here isn't available yet.")
  })

  it('shows how to teach one when there are none', async () => {
    api.listWorkflows.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/Nothing taught yet/)).toBeInTheDocument()
  })
})
