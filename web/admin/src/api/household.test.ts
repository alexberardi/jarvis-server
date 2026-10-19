import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { putHouseholdSetting } from './household'

const put = vi.spyOn(apiClient, 'put')

beforeEach(() => {
  vi.clearAllMocks()
  put.mockResolvedValue({ status: 200, data: {} })
})

describe('putHouseholdSetting', () => {
  it('PUTs the value under the key, dotted keys as they are', async () => {
    await putHouseholdSetting('h1', 'watches.enabled', true)
    expect(put).toHaveBeenCalledWith('/api/cc/api/v0/mobile/household/h1/settings/watches.enabled', { value: true })
  })

  it('encodes a key that is not URL-safe', async () => {
    await putHouseholdSetting('h1', 'a/b?c', false)
    expect(put).toHaveBeenCalledWith('/api/cc/api/v0/mobile/household/h1/settings/a%2Fb%3Fc', { value: false })
  })
})
