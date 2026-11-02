import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import { createRef } from 'react'
import SpeechWaveform from './SpeechWaveform'
afterEach(() => vi.unstubAllGlobals())
it('bursts outward in every quadrant without moving peaks around the ring', async () => {
  let send!: (level: number) => void
  const stream = new ReadableStream({ start(controller) { send = level => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ voice: 'speaking', level })}\n\n`)) } })
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, body: stream })))
  const { container } = render(<SpeechWaveform voice="idle" theme={createRef()} />)
  send(0.001)
  expect(await screen.findByRole('img', { name: /Circular waveform/ })).toBeInTheDocument()
  const flares = () => [...container.querySelectorAll('[data-flares] path')]
  expect(flares()).toHaveLength(8)
  const points = (path: Element) => (path.getAttribute('d')!.match(/-?[\d.]+/g) || []).map(Number)
  const radius = (p: number[]) => Math.max(...Array.from({ length: p.length / 2 }, (_, i) => Math.hypot(p[i * 2] - 120, p[i * 2 + 1] - 120)))
  const before = flares().map(points)
  send(0.64)
  await waitFor(() => expect(radius(points(flares()[7]))).toBeGreaterThan(radius(before[7]) + 5))
  const after = points(flares()[7])
  for (let i = 0; i < after.length; i += 2) {
    const angle = (p: number[]) => Math.atan2(p[i] - 120, p[i + 1] - 120)
    expect(Math.abs(angle(after) - angle(before[7]))).toBeLessThan(0.001)
  }
  for (let quadrant = 0; quadrant < 4; quadrant++) {
    const section = (p: number[]) => p.slice(quadrant * 128, (quadrant + 1) * 128)
    expect(radius(section(after))).toBeGreaterThan(radius(section(before[7])) + 5)
  }
  expect(container.querySelector('[transform*="rotate"]')).toBeNull()
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/voice-stream', expect.objectContaining({ headers: { 'X-Jarvis-Display': '1' } })))
})
it('keeps the flare shape static when reduced motion is preferred', async () => {
  let send!: (level: number) => void
  const stream = new ReadableStream({ start(controller) { send = level => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ voice: 'speaking', level })}\n\n`)) } })
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true })))
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, body: stream })))
  const { container } = render(<SpeechWaveform voice="idle" theme={createRef()} />)
  send(0.001)
  await screen.findByRole('img', { name: /Circular waveform/ })
  const before = container.querySelector('[data-flares]')!.innerHTML
  await act(async () => { send(0.99) })
  await waitFor(() => expect(container.querySelector('[data-flares]')!.innerHTML).toEqual(before))
})

it('uses the inherited strand colour and increases visible emphasis', async () => {
  const stream = new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode('data: {"voice":"speaking","level":0.1}\n\n')) } })
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, body: stream })))
  const theme = createRef<HTMLIFrameElement>()
  const { container, rerender } = render(<SpeechWaveform voice="idle" theme={theme} colour="#ff6600" emphasis={100} />)
  await screen.findByRole('img', { name: /Circular waveform/ })
  expect(container.querySelector('stop[offset=".85"]')).toHaveAttribute('stop-color', '#ff6600')
  const before = container.querySelector('[data-flares]')!.getAttribute('stroke-width')
  const path = container.querySelector('[data-flares] path')!.getAttribute('d')
  rerender(<SpeechWaveform voice="idle" theme={theme} colour="#ff6600" emphasis={175} />)
  expect(Number(container.querySelector('[data-flares]')!.getAttribute('stroke-width'))).toBeGreaterThan(Number(before))
  expect(container.querySelector('[data-flares] path')!.getAttribute('d')).not.toEqual(path)
})
