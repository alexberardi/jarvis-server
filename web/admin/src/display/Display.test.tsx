import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
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
