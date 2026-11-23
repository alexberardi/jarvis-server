import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import RoomsDialog from './RoomsDialog'

const api = vi.hoisted(() => ({ createRoom: vi.fn(), updateRoom: vi.fn(), deleteRoom: vi.fn() }))
vi.mock('@/api/devices', async (orig) => ({ ...(await orig<typeof import('@/api/devices')>()), ...api }))

const rooms = [
  { id: 'up', name: 'Upstairs', parent_room_id: null, device_count: 0 },
  { id: 'bed', name: 'Bedroom', parent_room_id: 'up', device_count: 3 },
  { id: 'closet', name: 'Closet', parent_room_id: 'bed', device_count: 0 },
  { id: 'kitchen', name: 'Kitchen', parent_room_id: null, device_count: 1 },
]

function renderDialog() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RoomsDialog household="h1" rooms={rooms} onClose={() => {}} />
    </QueryClientProvider>,
  )
}

describe('rooms dialog', () => {
  beforeEach(() => vi.clearAllMocks())

  it('lists rooms under their parents, with where they are and what they hold', () => {
    renderDialog()
    const items = within(screen.getByRole('list', { name: 'Rooms' })).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual([
      'Kitchen1 device',
      'Upstairs0 devices',
      'BedroomIn Upstairs · 3 devices',
      'ClosetIn Upstairs › Bedroom · 0 devices',
    ])
  })

  it('a room can’t be moved inside itself or a room inside it', () => {
    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Edit Bedroom' }))
    const inside = screen.getByLabelText('Inside')
    expect(
      within(inside)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Nothing (a top-level room)', 'Kitchen', 'Upstairs'])
  })

  it('moves a room to the top and says what deleting one takes with it', async () => {
    api.updateRoom.mockResolvedValue({})
    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: 'Edit Bedroom' }))
    fireEvent.change(screen.getByLabelText('Inside'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updateRoom).toHaveBeenCalledWith('h1', 'bed', { name: 'Bedroom', parent_room_id: null }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Bedroom' }))
    expect(screen.getByText('Delete Bedroom and the room inside it (Closet)? Its 3 devices stay, without a room.')).toBeInTheDocument()
  })
})
