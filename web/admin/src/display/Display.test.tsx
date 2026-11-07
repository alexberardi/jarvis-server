import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ShowClock, ShowDisplay } from './Display'

const config = { enabled: true, clock: 'combined', hour_24: true, timezone: 'Australia/Perth', idle_seconds: 120, slide_seconds: 30, photos: [], favourites: [], night_dim: false, dim_start: 22, dim_end: 7, dim_percent: 35, destination: 'local', shortcuts: [] }
const devices = Array.from({ length: 1000 }, (_, i) => ({ id: `d${i}`, name: i === 0 ? 'Front door' : `Light ${i}`, room_name: 'Living room', domain: i === 0 ? 'lock' : 'light', entity_id: `light.${i}`, supported_actions: [{ button_action: i === 0 ? 'unlock' : 'turn_on', button_text: i === 0 ? 'Unlock' : 'Turn on' }] }))
beforeEach(() => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value() { this.setAttribute('open', '') } })
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value() { this.removeAttribute('open') } })
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
it('renders both clock formats in the configured timezone', () => {
  render(<ShowClock now={new Date('2026-10-23T12:30:00Z')} config={config} />)
  expect(screen.getByText('20:30')).toBeInTheDocument()
  expect(screen.getByRole('img')).toHaveAccessibleName('20:30:00')
})
it('bounds a large catalogue and confirms access before sending an action', async () => {
  const actions: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    const path = url.replace('/api/', '')
    let body: unknown
    if (path === 'state') body = { configuration: config, voice: 'listening', last_interaction: Date.now(), playback: { active: false }, theme_url: '', receivers: {} }
    else if (path === 'devices') body = devices
    else if (path.endsWith('/control')) { actions.push(JSON.parse(init.body as string)); body = { success: true } }
    else body = { state: { state: 'locked' } }
    return { ok: true, text: async () => JSON.stringify(body) }
  }))
  render(<ShowDisplay />)
  fireEvent.click(await screen.findByRole('button', { name: 'Devices' }))
  expect(await screen.findByRole('heading', { name: 'Front door' })).toBeInTheDocument()
  expect(screen.getAllByRole('article')).toHaveLength(24)
  fireEvent.click(screen.getByRole('button', { name: 'Unlock' }))
  expect(actions).toHaveLength(0)
  fireEvent.click(screen.getByRole('button', { name: /^Confirm$/ }))
  await waitFor(() => expect(actions).toHaveLength(1))
  expect(actions[0]).toEqual({ action: 'unlock', confirmed: true })
  fireEvent.change(screen.getByLabelText('Search devices'), { target: { value: 'Light 999' } })
  expect(screen.getAllByRole('article')).toHaveLength(1)
})
it('keeps the confirmed state after a failed device action', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => ({ ok: !url.endsWith('/control'), text: async () => JSON.stringify(url.endsWith('/control') ? { error: 'Device unavailable' } : url === '/api/state' ? { configuration: config, voice: 'idle', last_interaction: Date.now(), playback: { active: false }, theme_url: '', receivers: {} } : url === '/api/devices' ? [devices[1]] : { state: { state: 'off' } }) })))
  render(<ShowDisplay />)
  fireEvent.click(await screen.findByRole('button', { name: 'Devices' }))
  expect(await screen.findByText('off')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Turn on' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Device unavailable')
  expect(screen.getByText('off')).toBeInTheDocument()
})

const catalogue = { version: 1, domains: { media_player: { label: 'Media player', visible: true, control_type: 'media' }, light: { label: 'Light', visible: true, control_type: 'light' }, update: { label: 'Update', visible: false, control_type: 'sensor' } }, fallback: { label: 'Device', visible: true, control_type: 'sensor' } }
it('follows a remote destination: its playback, its controls', async () => {
  const sent: Record<string, unknown>[] = []
  let remote = { active: true, kind: 'remote', controllable: true, paused: false, features: ['pause', 'play', 'stop'], media_title: 'Big Buck Bunny' }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace('/api/', '')
    let body: unknown = {}
    if (path === 'state') body = { configuration: config, voice: 'idle', last_interaction: Date.now(), playback: { active: false, kind: '', controllable: false }, theme_url: '', receivers: {} }
    else if (path === 'catalogue') body = catalogue
    else if (path === 'devices') body = [{ id: 'tv-1', name: 'Lounge TV', room_name: 'Lounge', domain: 'media_player', entity_id: 'media_player.lounge', supported_actions: [] }, { ...devices[1] }]
    else if (path === 'media') {
      const m = JSON.parse(init!.body as string)
      if (m.action === 'status') body = remote
      else { sent.push(m); if (m.action === 'pause') remote = { ...remote, paused: true }; body = { success: true } }
    }
    return { ok: true, text: async () => JSON.stringify(body) }
  }))
  render(<ShowDisplay />)
  fireEvent.click(await screen.findByRole('button', { name: 'Media' }))
  // Only media devices are destinations, by id.
  const select = await screen.findByLabelText('Playback destination')
  await waitFor(() => expect(screen.getByRole('option', { name: 'Lounge TV (Lounge)' })).toBeInTheDocument())
  expect(screen.queryByRole('option', { name: /Light 1/ })).toBeNull()
  expect(screen.getByText('Nothing playing')).toBeInTheDocument()
  fireEvent.change(select, { target: { value: 'tv-1' } })
  expect(await screen.findByText('Playing: Big Buck Bunny')).toBeInTheDocument()
  const pause = screen.getByRole('button', { name: 'Pause' })
  expect(pause).toBeEnabled()
  fireEvent.click(pause)
  await waitFor(() => expect(sent).toEqual([expect.objectContaining({ action: 'pause', destination: 'tv-1' })]))
  expect(await screen.findByText('Paused: Big Buck Bunny')).toBeInTheDocument()
  // A destination without seek offers no seek bar.
  expect(screen.queryByLabelText('Seek')).toBeNull()
})
it('hides device types the catalogue hides', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = url.replace('/api/', '')
    const body = path === 'state' ? { configuration: config, voice: 'idle', last_interaction: Date.now(), playback: { active: false }, theme_url: '', receivers: {} }
      : path === 'catalogue' ? catalogue
      : path === 'devices' ? [{ ...devices[1] }, { id: 'u1', name: 'Firmware', room_name: '', domain: 'update', entity_id: 'update.fw', supported_actions: null }]
      : { state: { state: 'off' } }
    return { ok: true, text: async () => JSON.stringify(body) }
  }))
  render(<ShowDisplay />)
  fireEvent.click(await screen.findByRole('button', { name: 'Devices' }))
  expect(await screen.findByRole('heading', { name: 'Light 1' })).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole('heading', { name: 'Firmware' })).toBeNull())
})
it('keeps device state requests at four through slow refreshes and page changes', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  let inFlight = 0, most = 0
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = url.replace('/api/', '')
    if (path.endsWith('/state')) {
      inFlight++; most = Math.max(most, inFlight)
      await new Promise(r => setTimeout(r, 10000)) // a slow plugin node
      inFlight--
      return { ok: true, text: async () => JSON.stringify({ state: { state: 'on' } }) }
    }
    const body = path === 'state' ? { configuration: config, voice: 'idle', last_interaction: Date.now(), playback: { active: false }, theme_url: '', receivers: {} }
      : path === 'devices' ? devices.slice(1, 49) : {}
    return { ok: true, text: async () => JSON.stringify(body) }
  }))
  try {
    render(<ShowDisplay />)
    fireEvent.click(await screen.findByRole('button', { name: 'Devices' }))
    expect(await screen.findByRole('heading', { name: 'Light 1' })).toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(16000)) // a refresh while the first batch is out
    fireEvent.click(screen.getByRole('button', { name: 'Next' })) // another 24 devices, mid-flight
    await act(() => vi.advanceTimersByTimeAsync(31000))
    expect(most).toBe(4)
  } finally { vi.useRealTimers() }
})

const np = (over: Record<string, unknown> = {}) => ({ source: 'pkg:radio', name: 'Radio', updated_at: new Date().toISOString(),
  state: { state: 'playing', media_title: 'So What', media_artist: 'Miles Davis', media_album: 'Kind of Blue', context: '', actions: ['play', 'pause', 'next', 'volume'], position: 30, duration: 545, volume: 40, ...over } })
function stubNowPlaying(nowPlaying: unknown, sent: Record<string, unknown>[], extra: Record<string, unknown> = {}, lastInteraction = Date.now()) {
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace('/api/', '')
    let body: unknown = {}
    if (path === 'state') body = { configuration: { ...config, ...extra }, voice: 'idle', last_interaction: lastInteraction, playback: { active: false }, theme_url: '', receivers: {}, now_playing: nowPlaying }
    else if (path === 'devices') body = []
    else if (path === 'now-playing') { const b = JSON.parse(init!.body as string); sent.push(b); body = { now_playing: nowPlaying } }
    return { ok: true, text: async () => JSON.stringify(body) }
  }))
}
it('shows what plays, with only the actions the source lists', async () => {
  const sent: Record<string, unknown>[] = []
  stubNowPlaying(np(), sent)
  render(<ShowDisplay />)
  const view = await screen.findByRole('region', { name: 'Now playing' })
  expect(view).toHaveTextContent('So What')
  expect(view).toHaveTextContent('Miles Davis')
  expect(view).toHaveTextContent('Playing on Radio')
  expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Next track' })).toBeInTheDocument()
  // Not listed by this source: no button.
  expect(screen.queryByRole('button', { name: 'Previous track' })).toBeNull()
  expect(screen.queryByRole('button', { name: /Shuffle|Repeat|Like/ })).toBeNull()
  // No seek action: progress only.
  expect(screen.getByRole('progressbar', { name: 'Position' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
  await waitFor(() => expect(sent).toEqual([{ action: 'pause' }]))
})
it('sends a slider once, when it is let go', async () => {
  const sent: Record<string, unknown>[] = []
  stubNowPlaying(np(), sent)
  render(<ShowDisplay />)
  const volume = await screen.findByRole('slider', { name: 'Volume' })
  fireEvent.change(volume, { target: { value: '55' } })
  fireEvent.change(volume, { target: { value: '60' } })
  expect(sent).toHaveLength(0)
  fireEvent.pointerUp(volume)
  await waitFor(() => expect(sent).toEqual([{ action: 'volume', volume: 60 }]))
})
it('Home hides the view until the next track, and the bar brings it back', async () => {
  stubNowPlaying(np(), [])
  render(<ShowDisplay />)
  fireEvent.click(await screen.findByRole('button', { name: 'Back to home' }))
  expect(screen.queryByRole('region', { name: 'Now playing' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /Now playing: So What/ }))
  expect(await screen.findByRole('region', { name: 'Now playing' })).toBeInTheDocument()
})
it('shows toggles with their state, and a blank cover when none loads', async () => {
  stubNowPlaying(np({ actions: ['pause', 'shuffle', 'repeat', 'like', 'seek', 'previous'], shuffle: true, repeat: 'one', liked: false, context: 'Late Jazz' }), [])
  render(<ShowDisplay />)
  expect(await screen.findByRole('button', { name: 'Shuffle' })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByRole('button', { name: 'Repeat one' })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByRole('button', { name: 'Like this track' })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('slider', { name: 'Position' })).toBeInTheDocument()
  expect(screen.getByText('Late Jazz')).toBeInTheDocument()
  const cover = screen.getByRole('img', { name: 'Cover of Kind of Blue' })
  fireEvent.error(cover)
  expect(screen.queryByRole('img', { name: 'Cover of Kind of Blue' })).toBeNull()
})
it('keeps the screen awake while playing, not while paused', async () => {
  // Fake timers: the display's clock is moved past the idle timeout, never waited for.
  vi.useFakeTimers({ shouldAdvanceTime: true })
  try {
    stubNowPlaying(np(), [], { idle_seconds: 60 })
    const { unmount } = render(<ShowDisplay />)
    expect(await screen.findByRole('region', { name: 'Now playing' })).toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(61000) })
    expect(screen.queryByRole('button', { name: 'Wake Jarvis display' })).toBeNull()
    unmount()
    stubNowPlaying(np({ state: 'paused' }), [], { idle_seconds: 60 })
    render(<ShowDisplay />)
    expect(await screen.findByRole('region', { name: 'Now playing' })).toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(61000) })
    expect(screen.getByRole('button', { name: 'Wake Jarvis display' })).toBeInTheDocument()
  } finally { vi.useRealTimers() }
})
