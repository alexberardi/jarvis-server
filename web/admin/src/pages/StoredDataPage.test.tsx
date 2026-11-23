import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import StoredDataPage from './StoredDataPage'

const api = vi.hoisted(() => ({
  listCommands: vi.fn(),
  getSchema: vi.fn(),
  browseRecords: vi.fn(),
  getRecord: vi.fn(),
  updateRecord: vi.fn(),
  createRecord: vi.fn(),
  deleteRecord: vi.fn(),
}))
vi.mock('@/api/commandData', () => api)
vi.mock('@/api/chat', () => ({ listChatNodes: vi.fn().mockResolvedValue([{ node_id: 'n1', name: 'Kitchen', room: 'kitchen' }]) }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

const schema = {
  mode: 'enabled',
  supports_create: true,
  fields: [
    { name: 'id', type: 'id', label: 'ID', editable: false },
    { name: 'title', type: 'string', label: 'Title', required: true },
    { name: 'priority', type: 'enum', label: 'Priority', enum_values: ['low', 'high'] },
    { name: 'done', type: 'bool', label: 'Done' },
    { name: 'tags', type: 'array', label: 'Tags' },
  ],
}

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/nodes/n1/data']}>
        <Routes>
          <Route path="/nodes/:id/data" element={<StoredDataPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('stored data', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listCommands.mockResolvedValue([
      { command_name: 'todo_list', mode: 'enabled', storage_name: 'todo' },
      { command_name: 'weather', mode: 'disabled', storage_name: 'weather' },
    ])
    api.getSchema.mockResolvedValue(schema)
    api.browseRecords.mockResolvedValue({ records: [{ key: 'r1', summary: { title: 'Buy milk', subtitle: 'high' }, data: {} }], truncated: false, count: 1 })
    api.getRecord.mockResolvedValue({ record: { id: 'r1', title: 'Buy milk', priority: 'high', done: false, tags: ['shop', 'today'] }, schema })
  })

  it('lists browsable commands and a record, and saves only what changed', async () => {
    api.updateRecord.mockResolvedValue(undefined)
    renderPage()
    expect(await screen.findByText('What Kitchen’s commands keep.')).toBeInTheDocument()
    expect(within(await screen.findByLabelText('Command')).queryByRole('option', { name: /Weather/ })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: /Buy milk/ }))
    const dialog = await screen.findByRole('dialog')
    // Not editable: the id (editable false) and the list (no list editor on the web).
    expect(await within(dialog).findByText('shop, today')).toBeInTheDocument()
    expect(within(dialog).queryByLabelText('ID')).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.click(within(dialog).getByRole('switch', { name: 'Done' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.updateRecord).toHaveBeenCalledWith('n1', 'todo_list', 'r1', { done: true }))
  })

  it('adds a record once its required fields are filled', async () => {
    api.createRecord.mockResolvedValue({ key: 'r2' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Add' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add to Todo list' })
    const add = within(dialog).getByRole('button', { name: 'Add' })
    fireEvent.click(within(dialog).getByRole('switch', { name: 'Done' }))
    expect(add).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText('Title (required)'), { target: { value: 'Call the plumber' } })
    fireEvent.click(add)
    await vi.waitFor(() => expect(api.createRecord).toHaveBeenCalledWith('n1', 'todo_list', { done: true, title: 'Call the plumber' }))
  })

  it('deletes a record after confirming', async () => {
    api.deleteRecord.mockResolvedValue(undefined)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Buy milk/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('shop, today')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    fireEvent.click(within(within(dialog).getByRole('group', { name: 'Delete this record' })).getByRole('button', { name: 'Delete' }))
    await vi.waitFor(() => expect(api.deleteRecord).toHaveBeenCalledWith('n1', 'todo_list', 'r1'))
  })

  it('says when the node doesn’t answer', async () => {
    api.listCommands.mockRejectedValue(Object.assign(new Error('timeout'), { isAxiosError: true, response: { status: 504 } }))
    renderPage()
    expect(await screen.findByText('The node didn’t answer. It may be offline.')).toBeInTheDocument()
  })
})
