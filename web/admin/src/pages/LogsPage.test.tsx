import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { getLogs, getLogSources } from '@/api/logs'
import { renderPage } from '@/test/renderPage'
import LogsPage from './LogsPage'

vi.mock('@/api/logs', async (orig) => ({
  ...(await orig<typeof import('@/api/logs')>()),
  getLogs: vi.fn(),
  getLogSources: vi.fn(),
  tailLogs: vi.fn(),
}))

const entry = { id: '1', timestamp: '2026-10-20T12:00:00Z', level: 'ERROR', service: 'cc', node_id: null, message: 'boom', context: null }

beforeEach(() => {
  vi.mocked(getLogSources).mockResolvedValue({ services: ['cc'], nodes: [] } as never)
})

describe('LogsPage states', () => {
  it('lists the entries', async () => {
    vi.mocked(getLogs).mockResolvedValue({ logs: [entry], next_cursor: null } as never)
    renderPage(<LogsPage />)
    expect(await screen.findByRole('list', { name: 'Log entries' })).toHaveTextContent('boom')
    expect(screen.getByRole('heading', { level: 1, name: 'Logs' })).toBeInTheDocument()
  })

  it('says nothing is logged yet when there are no entries and no filters', async () => {
    vi.mocked(getLogs).mockResolvedValue({ logs: [], next_cursor: null } as never)
    renderPage(<LogsPage />)
    expect(await screen.findByText('Nothing logged yet')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Clear filters' })).toBeNull()
  })

  it('offers to clear the filters when nothing matches them', async () => {
    vi.mocked(getLogs).mockResolvedValue({ logs: [], next_cursor: null } as never)
    renderPage(<LogsPage />)
    fireEvent.change(await screen.findByLabelText('Minimum level'), { target: { value: 'ERROR' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(screen.getByLabelText('Minimum level')).toHaveValue(''))
    expect(await screen.findByText('Nothing logged yet')).toBeInTheDocument()
  })

  it('shows the error with a retry', async () => {
    vi.mocked(getLogs).mockRejectedValueOnce(new Error('the log store is locked'))
    renderPage(<LogsPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't read the logs")
    expect(screen.getByRole('alert')).toHaveTextContent('the log store is locked')
    vi.mocked(getLogs).mockResolvedValue({ logs: [entry], next_cursor: null } as never)
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('list', { name: 'Log entries' })).toBeInTheDocument()
  })
})
