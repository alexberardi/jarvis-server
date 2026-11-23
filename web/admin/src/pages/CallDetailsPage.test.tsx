import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import CallDetailsPage from './CallDetailsPage'

const api = vi.hoisted(() => ({ getCallContext: vi.fn(), putCallContext: vi.fn() }))
vi.mock('@/api/callContext', () => api)

const catalog = {
  well_known: [
    { key: 'full_name', label: 'Full name', category: 'general', tier: 'state' },
    { key: 'insurance_member_id', label: 'Insurance member ID', category: 'medical', tier: 'if_asked' },
  ],
  categories: [
    { value: 'general', label: 'General' },
    { value: 'medical', label: 'Medical' },
    { value: 'dining', label: 'Dining & reservations' },
  ],
  tiers: [
    { value: 'state', label: 'May be said freely' },
    { value: 'if_asked', label: 'Only if they ask' },
  ],
}

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <CallDetailsPage />
    </QueryClientProvider>,
  )
}

describe('call details', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getCallContext.mockResolvedValue({
      fields: [{ key: 'full_name', label: 'Full name', value: 'Carl Mendes', category: 'general', tier: 'state' }],
      catalog,
    })
    api.putCallContext.mockImplementation(async (fields) => ({ fields, catalog }))
  })

  it('adds a common detail: the preset fills the name, category and tier', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Add detail' }))
    const dialog = screen.getByRole('dialog')
    const start = within(dialog).getByLabelText('Start from a common detail')
    // Full name is already saved, so it isn't offered again.
    expect(within(start).queryByRole('option', { name: 'Full name' })).toBeNull()
    fireEvent.change(start, { target: { value: 'insurance_member_id' } })
    expect(within(dialog).getByLabelText('Name')).toHaveValue('Insurance member ID')
    expect(within(dialog).getByLabelText('Category')).toHaveValue('medical')
    expect(within(dialog).getByLabelText('Only if they ask')).toBeChecked()
    fireEvent.change(within(dialog).getByLabelText('Value'), { target: { value: ' XQ-4471-09 ' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() =>
      expect(api.putCallContext).toHaveBeenCalledWith([
        { key: 'full_name', label: 'Full name', value: 'Carl Mendes', category: 'general', tier: 'state' },
        { key: 'insurance_member_id', label: 'Insurance member ID', value: 'XQ-4471-09', category: 'medical', tier: 'if_asked' },
      ]),
    )
    expect(await screen.findByText('XQ-4471-09')).toBeInTheDocument()
  })

  it('refuses a second detail with the same name', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Add detail' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'full name' } })
    fireEvent.change(within(dialog).getByLabelText('Value'), { target: { value: 'C. Mendes' } })
    expect(within(dialog).getByText('You already have a detail with that name')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('removes a detail by saving the rest', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove Full name' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    await vi.waitFor(() => expect(api.putCallContext).toHaveBeenCalledWith([]))
    expect(await screen.findByText('No details yet')).toBeInTheDocument()
  })
})
