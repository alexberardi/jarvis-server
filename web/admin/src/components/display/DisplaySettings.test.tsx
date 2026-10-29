import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { apiClient } from '@/api/client'
import DisplaySettings from './DisplaySettings'

const configuration = { waveform_colour: '#a56bff', waveform_emphasis: 150, enabled: false, theme: 'builtin', favourites: [], idle_seconds: 90, slide_seconds: 30, photos: [], clock: 'digital', hour_24: true, timezone: '', night_dim: false, dim_start: 22, dim_end: 7, dim_percent: 35, destination: 'local', shortcuts: [], playercast: false, experimental_cast: false, receiver_name: 'Jarvis Display' }
const response = { overrides: { idle_seconds: 90 }, configuration, inherited: { ...configuration, idle_seconds: 120 }, revision: 'saved', applied_revision: 'older', pin_configured: false, devices: [] }
beforeEach(() => vi.spyOn(apiClient, 'get').mockImplementation(async url => ({ data: String(url).includes('/assets') ? [] : response })))
afterEach(() => vi.restoreAllMocks())
it('removes an override and immediately shows the inherited value', async () => {
  const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: { ...response, overrides: {} } })
  render(<DisplaySettings scope="node1" label="Display settings" />)
  fireEvent.click(screen.getByText('Display settings'))
  const idle = await screen.findByLabelText('Idle timeout (seconds)')
  expect(idle).toHaveValue(90)
  fireEvent.click(screen.getByRole('button', { name: /^Use server default$/ }))
  expect(idle).toHaveValue(120)
  fireEvent.click(screen.getByRole('button', { name: 'Save display settings' }))
  await waitFor(() => expect(put).toHaveBeenCalledWith('/api/cc/api/v0/display/config/node1', { overrides: {} }))
  expect(screen.getByText('Saved. Nodes apply settings on reconnect or within 30 seconds.')).toBeInTheDocument()
})
it('accepts 4 to 20 numeric digits and clears the PIN after saving', async () => {
  const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: response })
  render(<DisplaySettings scope="defaults" label="Display defaults" />)
  fireEvent.click(screen.getByText('Display defaults'))
  const input = await screen.findByLabelText('Exit PIN')
  fireEvent.change(input, { target: { value: '123' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save display settings' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('4 to 20 digits')
  expect(put).not.toHaveBeenCalled()
  fireEvent.change(input, { target: { value: '12345678901234567890' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save display settings' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  await waitFor(() => expect(input).toHaveValue(''))
})

it('saves per-node strand colour and emphasis overrides', async () => {
  const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: response })
  render(<DisplaySettings scope="node1" label="Display settings" />)
  fireEvent.click(screen.getByText('Display settings'))
  fireEvent.change(await screen.findByLabelText('Speech strand colour'), { target: { value: '#ff6600' } })
  fireEvent.change(screen.getByLabelText('Speech waveform emphasis (%)'), { target: { value: '175' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save display settings' }))
  await waitFor(() => expect(put).toHaveBeenCalledWith('/api/cc/api/v0/display/config/node1', { overrides: { idle_seconds: 90, waveform_colour: '#ff6600', waveform_emphasis: 175 } }))
})
