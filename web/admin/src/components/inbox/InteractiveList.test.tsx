import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { InboxItem } from '@/api/inbox'
import { parsePayload } from '@/lib/inbox/interactiveList'
import InteractiveList from './InteractiveList'

const api = vi.hoisted(() => ({
  sendCallback: vi.fn(),
  callbackStatus: vi.fn(),
  listRecords: vi.fn(),
  updateRecord: vi.fn(),
  browserMatches: vi.fn(),
  getNodeLiveness: vi.fn(),
  getThisDevice: vi.fn(),
}))
vi.mock('@/api/callbacks', async (orig) => ({ ...(await orig<typeof import('@/api/callbacks')>()), sendCallback: api.sendCallback, callbackStatus: api.callbackStatus }))
vi.mock('@/api/commandData', async (orig) => ({ ...(await orig<typeof import('@/api/commandData')>()), listRecords: api.listRecords, updateRecord: api.updateRecord, browserMatches: api.browserMatches }))
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), getNodeLiveness: api.getNodeLiveness }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), getThisDevice: api.getThisDevice }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ state: { user: { id: 7 } }, activeHouseholdId: 'h1' }) }))

// The shopping-list export, as the Go node's export_shopping_list writes it.
const metadata = {
  type: 'interactive_list',
  version: 1,
  command_name: 'export_shopping_list',
  node_id: 'kitchen',
  context: { list: 'groceries' },
  sections: [
    {
      title: 'Ready for Walmart',
      rows: [
        { key: 'milk', label: 'Whole milk', control: 'checkbox_stepper', default: { selected: true, quantity: 2 } },
        {
          key: 'oats',
          label: 'Rolled oats',
          control: 'checkbox',
          default: { selected: true },
          disabled_caption: 'No Walmart item yet',
          requires_record_field: { command_name: 'export_shopping_list', field: 'walmart_item_id', field_label: 'ID' },
          row_actions: [
            { label: 'Find ID', type: 'webview_pick', start_url: 'https://www.walmart.com/search?q={label}', pattern: '/ip/(?:[^/]+/)?(\\d{5,})', save: { command_name: 'export_shopping_list', field: 'walmart_item_id' } },
          ],
        },
      ],
    },
  ],
  actions: [{ label: 'Add {n} to cart', callback: 'add_to_cart', style: 'primary' }],
}

const item = { id: 'l1', household_id: 'h1', title: 'Shopping list', category: 'interactive_list', metadata } as unknown as InboxItem

function renderList() {
  const parsed = parsePayload(metadata)
  if ('invalid' in parsed) throw new Error('fixture invalid')
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <InteractiveList item={item} payload={parsed.payload} truncated={parsed.truncated} />
    </QueryClientProvider>,
  )
}

describe('InteractiveList', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listRecords.mockResolvedValue([])
    api.getNodeLiveness.mockResolvedValue([])
    api.getThisDevice.mockResolvedValue({ node_id: null })
    api.sendCallback.mockResolvedValue({ id: 'job1', status: 'pending' })
    api.callbackStatus.mockResolvedValue({ id: 'job1', status: 'completed', navigation_type: 'stack', completed_at: null, error_message: null, context_data: { message: 'Added to your cart', detail_lines: ['Whole milk × 3'] } })
    vi.spyOn(window, 'open').mockReturnValue(null)
  })

  it('sends the selection with quantities to the node and shows the result', async () => {
    renderList()
    expect(await screen.findByLabelText('Whole milk')).toBeChecked()
    // The gated row has no record yet: unchecked, disabled, with its reason.
    // While the node's records load the gated row says so, then gives its reason.
    expect(screen.getByLabelText('Rolled oats')).toBeDisabled()
    expect(await screen.findByText('No Walmart item yet')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'One more Whole milk' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add 1 to cart' }))
    expect(await screen.findByText('Added to your cart', {}, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.getByText('Whole milk × 3')).toBeInTheDocument()
    expect(api.sendCallback).toHaveBeenCalledWith({
      command_name: 'export_shopping_list',
      callback_name: 'add_to_cart',
      data: { action: 'add_to_cart', selected: [{ key: 'milk', quantity: 3 }], context: { list: 'groceries' } },
      target_node_id: 'kitchen',
      navigation_type: 'stack',
    })
  })

  it('a stored record unlocks a gated row and shows its value', async () => {
    api.listRecords.mockResolvedValue([{ key: 'oats', data: { walmart_item_id: '10315752' } }])
    renderList()
    const oats = await screen.findByLabelText('Rolled oats')
    expect(screen.getByText('Checking…')).toBeInTheDocument()
    await waitFor(() => expect(oats).toBeEnabled())
    expect(oats).toBeChecked()
    expect(screen.getByText('ID: 10315752')).toBeInTheDocument()
    expect(api.listRecords).toHaveBeenCalledWith('kitchen', 'export_shopping_list')
  })

  it('Find ID opens the store and picks a value from the owner’s open tabs', async () => {
    api.getNodeLiveness.mockResolvedValue([
      { node_id: 'laptop', node_kind: 'desktop', owner_user_id: 7, online: true },
      { node_id: 'sams-pc', node_kind: 'desktop', owner_user_id: 8, online: true },
    ])
    api.browserMatches.mockResolvedValue({ ok: true, matches: [{ value: '10315752', title: 'Great Value Rolled Oats' }] })
    api.updateRecord.mockResolvedValue(undefined)
    renderList()
    fireEvent.click(await screen.findByRole('button', { name: 'Find ID' }))
    expect(window.open).toHaveBeenCalledWith('https://www.walmart.com/search?q=Rolled%20oats', '_blank', 'noopener')

    const use = await screen.findByRole('button', { name: 'Use 10315752' })
    expect(api.browserMatches).toHaveBeenCalledWith('laptop', '/ip/(?:[^/]+/)?(\\d{5,})')
    fireEvent.click(use)
    await waitFor(() => expect(screen.getByLabelText('Rolled oats')).toBeChecked())
    expect(api.updateRecord).toHaveBeenCalledWith('kitchen', 'export_shopping_list', 'oats', { walmart_item_id: '10315752' })
    expect(screen.getByText('ID: 10315752')).toBeInTheDocument()
  })

  it('without a computer to ask, a pasted address gives the value', async () => {
    api.updateRecord.mockResolvedValue(undefined)
    renderList()
    fireEvent.click(await screen.findByRole('button', { name: 'Find ID' }))
    const group = screen.getByRole('group', { name: /Find a value for Rolled oats/ })
    const box = within(group).getByLabelText('Paste the page’s address')
    fireEvent.change(box, { target: { value: 'https://www.walmart.com/search?q=oats' } })
    expect(within(group).getByText("That address doesn't have what this needs.")).toBeInTheDocument()
    fireEvent.change(box, { target: { value: 'https://www.walmart.com/ip/Great-Value-Oats/10315752?from=search' } })
    fireEvent.click(within(group).getByRole('button', { name: 'Use 10315752' }))
    await waitFor(() => expect(screen.getByLabelText('Rolled oats')).toBeEnabled())
    expect(api.browserMatches).not.toHaveBeenCalled()
  })
})
