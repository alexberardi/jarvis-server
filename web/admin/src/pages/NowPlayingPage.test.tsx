import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import NowPlayingPage from './NowPlayingPage'

const api = vi.hoisted(() => ({ getPlayback: vi.fn(), controlPlayback: vi.fn(), fetchCover: vi.fn() }))
vi.mock('@/api/playback', () => api)
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'h1' }) }))

const playing = {
  node_id: 'n1',
  node_name: 'Office',
  source: 'spotify',
  name: 'Spotify',
  updated_at: new Date().toISOString(),
  state: {
    state: 'playing',
    media_title: 'Holocene',
    media_artist: 'Bon Iver',
    media_album: 'Bon Iver',
    context: 'Focus mix',
    actions: ['pause', 'play', 'next', 'previous', 'shuffle', 'like'],
    shuffle: true,
    liked: false,
  },
}

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <NowPlayingPage />
    </QueryClientProvider>,
  )
}

describe('now playing', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getPlayback.mockResolvedValue([playing])
  })

  it('shows the track with its source’s controls, and a control’s read-back replaces the card', async () => {
    api.controlPlayback.mockResolvedValue({ ...playing, state: { ...playing.state, state: 'paused' } })
    renderPage()
    const card = await screen.findByRole('region', { name: 'Office: Holocene' })
    expect(within(card).getByText('Office · Focus mix')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Shuffle' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(card).queryByRole('button', { name: /Repeat/ })).toBeNull()
    // No cover, no request for one.
    expect(api.fetchCover).not.toHaveBeenCalled()
    fireEvent.click(within(card).getByRole('button', { name: 'Pause' }))
    await vi.waitFor(() => expect(api.controlPlayback).toHaveBeenCalledWith({ node_id: 'n1', action: 'pause' }))
    expect(await within(card).findByRole('button', { name: 'Play' })).toBeInTheDocument()
    expect(within(card).getByText('Paused')).toBeInTheDocument()
  })

  it('a stopped node’s card goes away', async () => {
    api.controlPlayback.mockResolvedValue(null)
    renderPage()
    const card = await screen.findByRole('region', { name: 'Office: Holocene' })
    fireEvent.click(within(card).getByRole('button', { name: 'Next track' }))
    expect(await screen.findByText('Nothing is playing')).toBeInTheDocument()
  })

  it('says so when a control fails', async () => {
    api.controlPlayback.mockRejectedValue(Object.assign(new Error('Node is offline'), {}))
    renderPage()
    const card = await screen.findByRole('region', { name: 'Office: Holocene' })
    fireEvent.click(within(card).getByRole('button', { name: 'Like this track' }))
    expect(await screen.findByText('That didn’t work')).toBeInTheDocument()
  })
})
