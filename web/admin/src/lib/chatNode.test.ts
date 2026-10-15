import { describe, expect, it } from 'vitest'
import type { NodeLiveness } from '@/api/nodes'
import { chatClient, nodeLabel, pickDefaultNode } from './chatNode'

const n = (node_id: string, online: boolean, room = node_id): NodeLiveness => ({
  node_id,
  room,
  household_id: 'h1',
  online,
  last_seen: null,
  last_seen_version: null,
  is_busy: false,
})

describe('pickDefaultNode: the default chain (webapp 01 §3.3)', () => {
  const nodes = [n('pi', true), n('agent', true), n('primary', true), n('off', false)]
  it.each([
    // [case, nodes, primary, thisDevice, remembered, want, reason]
    ['this computer’s agent first', nodes, 'primary', 'agent', '', 'agent', 'this-device'],
    ['an agent that is offline still wins: it is this computer', [n('agent', false), n('primary', true)], 'primary', 'agent', '', 'agent', 'this-device'],
    ['the primary when online', nodes, 'primary', '', '', 'primary', 'primary'],
    ['an offline primary is skipped for the first online', [n('primary', false), n('a', false), n('b', true)], 'primary', '', '', 'b', 'first-online'],
    ['no primary set: the first online', [n('a', false), n('b', true), n('c', true)], '', '', '', 'b', 'first-online'],
    ['an unknown primary id: the first online', [n('a', true)], 'gone', '', '', 'a', 'first-online'],
    ['nothing online: the primary', [n('a', false), n('primary', false)], 'primary', '', '', 'primary', 'offline-primary'],
    ['nothing online, no primary: the first', [n('a', false), n('b', false)], '', '', '', 'a', 'offline-first'],
    ['a remembered pick that is online beats the chain', nodes, 'primary', 'agent', 'pi', 'pi', 'remembered'],
    ['a remembered pick that is offline does not', nodes, 'primary', '', 'off', 'primary', 'primary'],
    ['a remembered pick that left the household does not', nodes, 'primary', '', 'gone', 'primary', 'primary'],
  ])('%s', (_, list, primaryId, thisDeviceId, rememberedId, want, reason) => {
    const got = pickDefaultNode({ nodes: list as NodeLiveness[], primaryId, thisDeviceId, rememberedId })
    expect(got.node?.node_id).toBe(want)
    expect(got.reason).toBe(reason)
  })

  it('has nothing to pick with no nodes', () => {
    expect(pickDefaultNode({ nodes: [], primaryId: 'x', thisDeviceId: '', rememberedId: '' })).toEqual({ node: null, reason: 'none' })
  })
})

describe('nodeLabel', () => {
  it("names a computer by its name, then the room, then a short id; a Pi keeps its room", () => {
    expect(nodeLabel({ node_id: 'abcdef123456', room: 'desktop', name: 'Office-PC', node_kind: 'desktop' })).toBe('Office-PC')
    expect(nodeLabel({ node_id: 'abcdef123456', room: 'desktop', name: ' ', node_kind: 'desktop' })).toBe('desktop')
    expect(nodeLabel({ node_id: 'abcdef123456', room: 'kitchen', name: 'pi-zero', node_kind: null })).toBe('kitchen')
    expect(nodeLabel({ node_id: 'abcdef123456', room: 'living_room' })).toBe('living room')
    expect(nodeLabel({ node_id: 'abcdef123456', room: 'default' })).toBe('abcdef12')
  })
})

describe('chatClient: the same-device flag (webapp 01 §3.6)', () => {
  it.each([
    ['the chosen node is this computer', 'laptop', 'laptop', true],
    ['another node', 'kitchen-pi', 'laptop', false],
    ["the server couldn't tell", 'laptop', '', false],
    ['no node chosen', '', '', false],
  ])('%s', (_, nodeId, thisDeviceId, same) => {
    expect(chatClient(nodeId, thisDeviceId)).toEqual({ surface: 'web', same_device: same })
  })
})
