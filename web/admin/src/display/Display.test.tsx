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
