import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { requestRestart, requestStop } from './system'
import { featureMissing, resetFeatures } from '@/lib/features'
import { restartAction } from '@/lib/settings'

const post = vi.spyOn(apiClient, 'post')

function httpError(status: number, data: unknown) {
  return Object.assign(new Error(String(status)), { isAxiosError: true, response: { status, data } })
}

beforeEach(() => {
  vi.clearAllMocks()
  resetFeatures()
})

describe('restart (AD8, feature-detected)', () => {
  it('202 means a supervisor brings jarvisd back', async () => {
    post.mockResolvedValue({ status: 202, data: { restarting: true } })
    expect(await requestRestart()).toEqual({ kind: 'restarting' })
    expect(restartAction()).not.toBeNull()
  })

  it('409 carries the command to run by hand', async () => {
    post.mockRejectedValue(httpError(409, { detail: 'not supervised', command: 'jarvisd serve' }))
    expect(await requestRestart()).toEqual({ kind: 'manual', detail: 'not supervised', command: 'jarvisd serve' })
    expect(restartAction()).not.toBeNull()
  })

  it('404 (an older jarvisd) hides the restart action', async () => {
    post.mockRejectedValue(httpError(404, { detail: 'Not found' }))
    expect(await requestRestart()).toEqual({ kind: 'unsupported' })
    expect(featureMissing('restart')).toBe(true)
    expect(restartAction()).toBeNull()
  })
})

describe('stop (AD8b)', () => {
  it('202 carries the command that starts jarvisd again', async () => {
    post.mockResolvedValue({ status: 202, data: { stopping: true, start_command: 'jarvisd service start --user', start_note: 'n' } })
    expect(await requestStop()).toEqual({ kind: 'stopping', startCommand: 'jarvisd service start --user', startNote: 'n' })
    expect(post).toHaveBeenCalledWith('/api/system/stop')
  })

  it('409 carries the reason and the fix', async () => {
    post.mockRejectedValue(httpError(409, { detail: 'old unit', command: 'sudo jarvisd service install' }))
    expect(await requestStop()).toEqual({ kind: 'refused', detail: 'old unit', command: 'sudo jarvisd service install' })
  })

  it('404 is an older jarvisd', async () => {
    post.mockRejectedValue(httpError(404, { detail: 'Not found' }))
    expect(await requestStop()).toEqual({ kind: 'unsupported' })
  })
})
