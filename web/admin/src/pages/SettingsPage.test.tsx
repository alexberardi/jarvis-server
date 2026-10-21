import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import { getAllSettings } from '@/api/settings'
import { renderPage } from '@/test/renderPage'
import type { AggregatedSettingsResponse } from '@/types/settings'
import SettingsPage from './SettingsPage'

vi.mock('@/api/settings', async (orig) => ({
  ...(await orig<typeof import('@/api/settings')>()),
  getAllSettings: vi.fn(),
}))

const data = {
  services: [
    {
      service_name: 'cc',
      display_name: 'Command center',
      success: true,
      error: null,
      latency_ms: 3,
      settings: [
        { key: 'cc.wake_word', value: 'jarvis', value_type: 'string', category: 'voice', description: 'The word that wakes a node', from_db: false, is_secret: false, requires_reload: true },
      ],
    },
  ],
  total_services: 1,
  successful_services: 1,
  failed_services: 0,
} as unknown as AggregatedSettingsResponse

describe('SettingsPage', () => {
  it('shows each module, the first one open', async () => {
    vi.mocked(getAllSettings).mockResolvedValue(data)
    renderPage(<SettingsPage />)
    expect(await screen.findByRole('button', { name: /Command center/, expanded: true })).toBeInTheDocument()
    expect(screen.getByText('cc.wake_word')).toBeInTheDocument()
    expect(screen.getByText('Needs restart')).toBeInTheDocument()
    expect(screen.getByText('1 of 1 modules loaded')).toBeInTheDocument()
  })

  it('says when nothing matches the search, and clears it', async () => {
    vi.mocked(getAllSettings).mockResolvedValue(data)
    renderPage(<SettingsPage />)
    fireEvent.change(await screen.findByLabelText('Search settings'), { target: { value: 'no-such-setting' } })
    expect(screen.getByText('No settings match')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Clear search' }))
    expect(screen.getByText('cc.wake_word')).toBeInTheDocument()
  })

  it('shows the error with a retry', async () => {
    vi.mocked(getAllSettings).mockRejectedValueOnce(new Error('the server is starting'))
    renderPage(<SettingsPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load settings")
    vi.mocked(getAllSettings).mockResolvedValue(data)
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('cc.wake_word')).toBeInTheDocument()
  })
})
