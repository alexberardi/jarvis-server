import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'
import PhonebookPage from './PhonebookPage'

const api = vi.hoisted(() => ({ listPhoneContacts: vi.fn(), createPhoneContact: vi.fn(), updatePhoneContact: vi.fn(), deletePhoneContact: vi.fn() }))
vi.mock('@/api/phoneContacts', async (orig) => ({ ...(await orig<typeof import('@/api/phoneContacts')>()), ...api }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

const tony = { id: 'c1', name: 'Tony’s Pizzeria', number: '+15551234567', address: '12 Main St', source: 'call', line_type: 'landline', do_not_call: false, notes: 'ask for Maria', created_at: '2026-11-01T10:00:00' }
const abc = { id: 'c2', name: 'ABC Plumbing', number: '+442071234567', address: null, source: 'manual', line_type: null, do_not_call: true, notes: null, created_at: '2026-11-02T10:00:00' }

const rejectedWith = (status: number, detail: string) =>
  new AxiosError('rejected', 'ERR', undefined, undefined, { status, statusText: '', headers: {}, config: { headers: new AxiosHeaders() }, data: { detail } })

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <PhonebookPage />
    </QueryClientProvider>,
  )
}

describe('PhonebookPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listPhoneContacts.mockResolvedValue([tony, abc])
    api.createPhoneContact.mockResolvedValue(tony)
    api.updatePhoneContact.mockResolvedValue(tony)
    api.deletePhoneContact.mockResolvedValue(undefined)
  })

  it('lists businesses by name with their number, address and badges', async () => {
    renderPage()
    const items = await screen.findAllByRole('listitem')
    expect(items[0]).toHaveTextContent('ABC Plumbing')
    expect(items[0]).toHaveTextContent('Do not call')
    expect(items[0]).not.toHaveTextContent('Added by you')
    expect(items[1]).toHaveTextContent('(555) 123-4567')
    expect(items[1]).toHaveTextContent('12 Main St')
    expect(items[1]).toHaveTextContent('Saved from a call')
  })

  it('adds one, showing cc’s reason under the field it refused', async () => {
    api.createPhoneContact.mockRejectedValueOnce(rejectedWith(400, 'That number has too few digits.'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Add business' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Joe’s Garage' } })
    fireEvent.change(within(dialog).getByLabelText('Phone number'), { target: { value: '555' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByText('That number has too few digits.')).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText('Phone number'), { target: { value: '+1 555 987 6543' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.createPhoneContact).toHaveBeenLastCalledWith('h1', { name: 'Joe’s Garage', number: '+1 555 987 6543', address: '', notes: '' }))
  })

  it('edits one, including Do not call', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit Tony’s Pizzeria' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/Dials as \(555\) 123-4567 · landline/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('switch', { name: /Do not call/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updatePhoneContact).toHaveBeenCalledWith('h1', 'c1', { name: 'Tony’s Pizzeria', number: '+15551234567', address: '12 Main St', notes: 'ask for Maria', do_not_call: true }))
  })

  it('Remove asks first', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove ABC Plumbing' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    await vi.waitFor(() => expect(api.deletePhoneContact).toHaveBeenCalledWith('h1', 'c2'))
  })
})
