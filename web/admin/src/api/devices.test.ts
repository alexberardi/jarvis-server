import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { controlDevice, importDevices, requestDeviceJob } from './devices'

vi.mock('./client', () => ({ apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() } }))
beforeEach(() => { vi.clearAllMocks(); vi.useFakeTimers() })
afterEach(() => vi.useRealTimers())

it('polls only its node/request and returns completed results', async () => {
  vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'request/1' } })
  vi.mocked(apiClient.get).mockResolvedValueOnce({ data: { status: 'pending' } })
    .mockResolvedValueOnce({ data: { status: 'completed', devices: [{ name: 'Desk' }] } })
  const job = requestDeviceJob('node/1', 'device-scan', new AbortController().signal)
  await vi.advanceTimersByTimeAsync(2000)
  expect((await job).devices).toEqual([{ name: 'Desk' }])
  expect(apiClient.get).toHaveBeenLastCalledWith('/api/cc/api/v0/nodes/node%2F1/device-scan/request%2F1', expect.objectContaining({ signal: expect.any(AbortSignal) }))
})

it.each([401, 403, 404, 410])('stops polling on HTTP %s', async (status) => {
  vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'r' } })
  vi.mocked(apiClient.get).mockRejectedValue({ response: { status } })
  const job = requestDeviceJob('n', 'device-scan', new AbortController().signal)
  await expect(job).rejects.toBeDefined()
  await vi.advanceTimersByTimeAsync(5000)
  expect(apiClient.get).toHaveBeenCalledTimes(1)
})

it('cancels pending polling when the view closes', async () => {
  vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'r' } })
  vi.mocked(apiClient.get).mockResolvedValue({ data: { status: 'pending' } })
  const controller = new AbortController()
  const job = requestDeviceJob('n', 'device-scan', controller.signal)
  const rejected = expect(job).rejects.toBeDefined()
  await vi.advanceTimersByTimeAsync(0)
  controller.abort()
  await rejected
  await vi.advanceTimersByTimeAsync(5000)
  expect(apiClient.get).toHaveBeenCalledTimes(1)
})

it('bounds pending jobs to two minutes', async () => {
  vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'r' } })
  vi.mocked(apiClient.get).mockResolvedValue({ data: { status: 'pending' } })
  const failed = expect(requestDeviceJob('n', 'device-list', new AbortController().signal)).rejects.toThrow(/expired/i)
  await vi.advanceTimersByTimeAsync(120000)
  await failed
})

it('imports with the selected node and allows the server pairing wait', async () => {
  vi.mocked(apiClient.post).mockResolvedValue({ data: { success: true } })
  const signal = new AbortController().signal
  await importDevices('h', 'n', [], signal)
  expect(apiClient.post).toHaveBeenLastCalledWith('/api/cc/api/v0/households/h/devices/import', { node_id: 'n', devices: [] }, expect.objectContaining({ signal }))
  await controlDevice('h', 'd', 'pair_finish', { session_id: 's', pin: '0123' }, signal)
  expect(apiClient.post).toHaveBeenLastCalledWith('/api/cc/api/v0/households/h/devices/d/control', { action: 'pair_finish', data: { session_id: 's', pin: '0123' } }, expect.objectContaining({ timeout: 30000, signal }))
})
