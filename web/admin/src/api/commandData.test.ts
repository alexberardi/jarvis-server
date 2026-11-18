import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { createRecord, updateRecord } from './commandData'

const patch = vi.spyOn(apiClient, 'patch')
const post = vi.spyOn(apiClient, 'post')

beforeEach(() => vi.clearAllMocks())

describe('command data writes', () => {
  // cc reads the overlay from {patch}: sent bare, the node got an empty patch and saved nothing.
  it('sends an update as {patch}', async () => {
    patch.mockResolvedValue({ data: { record: {} } })
    await updateRecord('kitchen', 'export_shopping_list', 'oats', { walmart_item_id: '10315752' })
    expect(patch).toHaveBeenCalledWith('/api/cc/api/v0/mobile/command-data/nodes/kitchen/commands/export_shopping_list/records/oats', {
      patch: { walmart_item_id: '10315752' },
    })
  })

  it('sends a create as {data}', async () => {
    post.mockResolvedValue({ data: { key: 'k1', record: {} } })
    expect(await createRecord('kitchen', 'notes', { title: 'Milk' })).toEqual({ key: 'k1', record: {} })
    expect(post).toHaveBeenCalledWith('/api/cc/api/v0/mobile/command-data/nodes/kitchen/commands/notes/records', { data: { title: 'Milk' } })
  })
})
