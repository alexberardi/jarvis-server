import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import MemoriesPage from './MemoriesPage'

const api = vi.hoisted(() => ({ listMemories: vi.fn(), createMemory: vi.fn(), updateMemory: vi.fn(), deleteMemory: vi.fn() }))
vi.mock('@/api/memories', async (orig) => ({ ...(await orig<typeof import('@/api/memories')>()), ...api }))
const auth = vi.hoisted(() => ({ role: 'member' }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1', households: [{ id: 'h1', name: 'Home', role: auth.role }] }) }))

const mem = (over: Record<string, unknown>) => ({ id: 1, user_id: 7, household_id: 'h1', category: 'preference', content: 'Likes oat milk', source: 'ui', is_pinned: false, created_at: '2026-11-01T10:00:00', editable: true, ...over })

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoriesPage />
    </QueryClientProvider>,
  )
}

describe('MemoriesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    auth.role = 'member'
    api.listMemories.mockResolvedValue([
      mem({}),
      mem({ id: 2, content: 'Weather: rain tomorrow', source: 'agent', category: 'agent_context', editable: false, is_pinned: true }),
      mem({ id: 3, user_id: null, content: 'Wifi password is on the fridge', category: 'fact' }),
    ])
    api.createMemory.mockResolvedValue(mem({ id: 9 }))
    api.updateMemory.mockResolvedValue(mem({}))
    api.deleteMemory.mockResolvedValue(undefined)
  })

  it('a member sees their own memories; read-only ones have no actions', async () => {
    renderPage()
    const yours = await screen.findByRole('region', { name: 'Yours' })
    expect(within(yours).getByText('Likes oat milk')).toBeInTheDocument()
    expect(within(yours).getByText('Read-only')).toBeInTheDocument()
    expect(within(yours).getByText('Pinned')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Edit: Weather/ })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Household' })).toBeNull()
  })

  it('an admin also sees the household’s and can add one for the household', async () => {
    auth.role = 'admin'
    renderPage()
    expect(await screen.findByRole('region', { name: 'Household' })).toHaveTextContent('Wifi password is on the fridge')
    fireEvent.click(screen.getByRole('button', { name: 'New memory' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('What should Jarvis remember?'), { target: { value: 'Trash goes out on Tuesday' } })
    fireEvent.click(within(dialog).getByLabelText('Household'))
    fireEvent.click(within(dialog).getByLabelText('Note'))
    fireEvent.click(within(dialog).getByRole('switch', { name: /Pin it/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.createMemory).toHaveBeenCalledWith('h1', { content: 'Trash goes out on Tuesday', category: 'note', is_pinned: true, scope: 'household' }))
  })

  it('a member can’t choose whose it is; edits keep their memory', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'New memory' }))
    expect(within(screen.getByRole('dialog')).queryByText('Whose')).toBeNull()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))

    fireEvent.click(screen.getByRole('button', { name: 'Edit: Likes oat milk' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('What should Jarvis remember?'), { target: { value: 'Likes oat milk, no sugar' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updateMemory).toHaveBeenCalledWith('h1', 1, { content: 'Likes oat milk, no sugar', category: 'preference', is_pinned: false }))
  })

  it('Forget asks first', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Forget: Likes oat milk' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Forget' }))
    await vi.waitFor(() => expect(api.deleteMemory).toHaveBeenCalledWith('h1', 1))
  })
})
