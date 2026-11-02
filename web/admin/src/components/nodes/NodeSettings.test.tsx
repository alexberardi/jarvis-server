import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { apiClient } from '@/api/client'
import NodeSettings from './NodeSettings'

const get = vi.spyOn(apiClient, 'get')
const patch = vi.spyOn(apiClient, 'patch')
const post = vi.spyOn(apiClient, 'post')
const view = { values: { wake_ack_audio_enabled: true, follow_up_listen_seconds: 3 }, fields: [
  { key: 'wake_ack_audio_enabled', label: 'Wake acknowledgment audio', group: 'Voice', type: 'boolean', min: 0, max: 0 },
  { key: 'follow_up_listen_seconds', label: 'Follow-up duration', group: 'Voice', type: 'number', min: 0, max: 3, step: .1 },
] }
beforeEach(() => { vi.clearAllMocks(); get.mockResolvedValue({ data: view }); patch.mockResolvedValue({ data: { ...view, values: { ...view.values, follow_up_listen_seconds: 1.5 } } }) })
it('reads ordinary config and saves only edited values', async () => {
  render(<NodeSettings nodeId="pc1" name="Office" online desktop />)
  fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
  const field = await screen.findByLabelText('Follow-up duration')
  fireEvent.change(field, { target: { value: '1.5' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(patch).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pc1/node-config', { settings: { follow_up_listen_seconds: 1.5 } }))
  expect(await screen.findByText(/Saved on the node/)).toBeInTheDocument()
  expect(get.mock.calls.every(([url]) => String(url).includes('/node-config'))).toBe(true)
})
it('keeps unsaved edits when saving fails and never reports success', async () => {
  patch.mockRejectedValue(new Error('Node did not confirm'))
  render(<NodeSettings nodeId="pc1" name="Office" online desktop />)
  fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
  fireEvent.change(await screen.findByLabelText('Follow-up duration'), { target: { value: '1.5' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Node did not confirm')
  expect(screen.getByLabelText('Follow-up duration')).toHaveValue(1.5)
  expect(screen.queryByText(/Saved on the node/)).toBeNull()
})
it('does not offer writes when the node is offline', async () => {
  render(<NodeSettings nodeId="pc1" name="Office" online={false} desktop />)
  expect(screen.getByRole('button', { name: 'Settings' })).toBeDisabled()
  expect(get).not.toHaveBeenCalled()
})
it('offers explicit unconfirmed legacy updates with no assumed values', async () => {
 get.mockRejectedValueOnce({ isAxiosError: true, response: { status: 504, data: { detail: 'Node did not confirm' } } })
 get.mockResolvedValueOnce({ data: { ...view, values: {} } })
 post.mockResolvedValueOnce({ data: { status: 'sent' } })
 render(<NodeSettings nodeId="pi1" name="Kitchen" online desktop={false} />)
 fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
 fireEvent.click(await screen.findByRole('button', { name: 'Set basic values without readback' }))
 const field = await screen.findByLabelText('Follow-up duration')
 expect(field).toHaveValue(null)
 expect(screen.getByLabelText('Wake acknowledgment audio')).toHaveValue('')
 fireEvent.change(field, { target: { value: '1.5' } })
 fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
 await waitFor(() => expect(post).toHaveBeenCalledWith('/api/cc/api/v0/nodes/pi1/node-config/basic', { settings: { follow_up_listen_seconds: 1.5 } }))
 expect(await screen.findByRole('status')).toHaveTextContent('cannot confirm')
 expect(patch).not.toHaveBeenCalled()
})
it('shows command-line overrides as read-only', async () => {
 get.mockResolvedValueOnce({ data: { ...view, read_only: ['follow_up_listen_seconds'] } })
 render(<NodeSettings nodeId="pc1" name="Office" online desktop />)
 fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
 expect(await screen.findByLabelText('Follow-up duration')).toBeDisabled()
})
