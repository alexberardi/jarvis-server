import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { Recording } from '@/api/workflows'
import { expiresIn } from '@/lib/workflows'
import RecordingsPage from './RecordingsPage'

const api = vi.hoisted(() => ({ listRecordings: vi.fn(), getRecording: vi.fn(), deleteRecording: vi.fn() }))
const household = vi.hoisted(() => ({ getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn() }))
const auth = vi.hoisted(() => ({ listHouseholds: vi.fn() }))
vi.mock('@/api/workflows', async (orig) => ({ ...(await orig<typeof import('@/api/workflows')>()), ...api }))
vi.mock('@/api/household', () => household)
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), ...auth }))

const DAY = 86_400_000
const rec = (over: Partial<Recording> = {}): Recording => ({
  id: 'rec_1',
  household_id: 'h1',
  owner_id: 7,
  node_id: 'n1',
  mode: 'browser',
  narration: '',
  keyframes: 0,
  steps: 3,
  created_at: new Date(Date.now() - DAY).toISOString(),
  expires_at: new Date(Date.now() + 12.5 * DAY).toISOString(),
  ...over,
})

function jwt(household: string): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64({ sub: '7', household_id: household })}.sig`
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <RecordingsPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', jwt('h1'))
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', is_superuser: false }))
  auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'member' }])
  household.getHouseholdSettings.mockResolvedValue({ 'computer.recording_retention_days': 30 })
  household.putHouseholdSetting.mockResolvedValue(undefined)
  api.deleteRecording.mockResolvedValue(undefined)
})

describe('RecordingsPage', () => {
  it('lists the recordings with mode, steps and expiry, and views one', async () => {
    api.listRecordings.mockResolvedValue([rec(), rec({ id: 'rec_2', mode: 'desktop', steps: 1, keyframes: 2 })])
    api.getRecording.mockResolvedValue({
      ...rec({ id: 'rec_2', mode: 'desktop', steps: 1, keyframes: 2 }),
      narration: 'this is how I get the statement',
      trace: [
        { kind: 'navigate', url: 'https://bank.example.com/statements' },
        { kind: 'click', element: { role: 'button', name: 'Download CSV' } },
        { kind: 'input', element: { name: 'Password' }, masked: true },
      ],
    })
    renderPage()
    const items = await screen.findAllByTestId('recording')
    expect(api.listRecordings).toHaveBeenCalledWith('h1')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('Browser')
    expect(items[0]).toHaveTextContent('3 steps')
    expect(items[0]).toHaveTextContent('Deleted in 12 days')
    expect(items[1]).toHaveTextContent('Desktop')
    expect(items[1]).toHaveTextContent('1 step')

    fireEvent.click(within(items[1]).getAllByRole('button')[0])
    expect(api.getRecording).toHaveBeenCalledWith('rec_2')
    expect(await within(items[1]).findByText('Opened bank.example.com')).toBeInTheDocument()
    expect(within(items[1]).getByText('Download CSV').tagName).toBe('STRONG')
    expect(within(items[1]).getByText(/Typed \(hidden\) in/)).toBeInTheDocument()
    expect(within(items[1]).getByText(/this is how I get the statement/)).toBeInTheDocument()
    expect(within(items[1]).getByText('2 keyframes, kept only on the computer that recorded it.')).toBeInTheDocument()
  })

  it('deletes a recording after confirming', async () => {
    api.listRecordings.mockResolvedValue([rec()])
    renderPage()
    const [item] = await screen.findAllByTestId('recording')
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true)
    const del = within(item).getByRole('button', { name: /^Delete Recording from/ })
    fireEvent.click(del)
    expect(api.deleteRecording).not.toHaveBeenCalled()
    api.listRecordings.mockResolvedValue([])
    await act(async () => {
      fireEvent.click(del)
    })
    expect(confirm).toHaveBeenCalledTimes(2)
    expect(api.deleteRecording.mock.calls[0][0]).toBe('rec_1')
    expect(await screen.findByText('No recordings.')).toBeInTheDocument()
  })

  it('shows retention read-only to a member', async () => {
    api.listRecordings.mockResolvedValue([])
    renderPage()
    const section = await screen.findByRole('region', { name: 'How long recordings are kept' })
    expect(await within(section).findByText('30 days.')).toBeInTheDocument()
    expect(within(section).getByText('Only a household admin can change this.')).toBeInTheDocument()
    expect(within(section).queryByLabelText('Keep for')).not.toBeInTheDocument()
  })

  it('lets a household admin change retention', async () => {
    auth.listHouseholds.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'owner' }])
    api.listRecordings.mockResolvedValue([])
    renderPage()
    const section = await screen.findByRole('region', { name: 'How long recordings are kept' })
    const days = await within(section).findByLabelText('Keep for')
    expect(days).toHaveValue(30)
    const saveBtn = within(section).getByRole('button', { name: 'Save' })
    fireEvent.change(days, { target: { value: '0' } })
    expect(saveBtn).toBeDisabled()
    fireEvent.change(days, { target: { value: '14' } })
    await act(async () => {
      fireEvent.click(saveBtn)
    })
    expect(household.putHouseholdSetting).toHaveBeenCalledWith('h1', 'computer.recording_retention_days', 14)
    expect(days).toHaveValue(14)
  })
})

describe('expiresIn', () => {
  it('says when in words', () => {
    const now = Date.parse('2026-10-19T12:00:00Z')
    expect(expiresIn('2026-10-19T18:00:00Z', now)).toBe('today')
    expect(expiresIn('2026-10-20T13:00:00Z', now)).toBe('tomorrow')
    expect(expiresIn('2026-11-18T12:00:00Z', now)).toBe('in 30 days')
  })
})
