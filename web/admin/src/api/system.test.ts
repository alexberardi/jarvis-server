import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { requestRestart } from './system'
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
