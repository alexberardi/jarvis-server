import { describe, expect, it } from 'vitest'
import type { StoreDetail } from '@/api/store'
import type { NodeLiveness } from '@/api/nodes'
import { aptPackages, nodeNames, nodeRoom, notForComputer, parseBundle, withBundle } from './store'

const pkg = (over: Partial<StoreDetail>): StoreDetail => ({
  command_name: 'doorbell',
  display_name: 'Doorbell',
  description: '',
  author: null,
  latest_version: '1.0.0',
  categories: [],
  verified: false,
  github_repo_url: 'https://github.com/acme/doorbell',
  git_tag: 'v1.0.0',
  ...over,
})

describe('store', () => {
  it('reads and edits the bundle', () => {
    expect(parseBundle('')).toEqual([])
    expect(parseBundle(null)).toEqual([])
    expect(parseBundle('a,b')).toEqual(['a', 'b'])
    expect(withBundle(['a', 'b'], 'c', true)).toBe('a,b,c')
    expect(withBundle(['a', 'b'], 'a', true)).toBe('b,a')
    expect(withBundle(['a', 'b'], 'a', false)).toBe('b')
    expect(withBundle(['a'], 'a', false)).toBe('')
  })

  it('knows what a computer cannot run, as cc does', () => {
    expect(notForComputer(pkg({}))).toBeNull()
    expect(notForComputer(pkg({ targets: [] }))).toBeNull()
    expect(notForComputer(pkg({ targets: ['node', 'desktop'] }))).toBeNull()
    expect(notForComputer(pkg({ targets: ['node'] }))).toBe('Doorbell is made for Pi nodes, not computers')
    expect(notForComputer(pkg({ apt_packages: ['ffmpeg'] }))).toBe('Doorbell needs system packages that only a Pi node installs')
  })

  it('lists the system packages once', () => {
    expect(aptPackages([pkg({ apt_packages: ['ffmpeg'] }), pkg({ apt_packages: ['ffmpeg', 'sox'] }), pkg({})])).toEqual([
      'ffmpeg',
      'sox',
    ])
  })

  it('names a node by its registered name, with its room as a second line', () => {
    const pi = { node_id: 'aaaa1111', room: 'living_room', name: 'dev-living-room', node_kind: null } as NodeLiveness
    expect(nodeNames([pi]).get('aaaa1111')).toBe('dev-living-room')
    expect(nodeRoom(pi)).toBe('living room')
    // No name: the room is the name, and isn't repeated.
    const unnamed = { ...pi, name: null }
    expect(nodeNames([unnamed]).get('aaaa1111')).toBe('living room')
    expect(nodeRoom(unnamed)).toBeNull()
    // A computer's room is always "desktop": not worth a line.
    expect(nodeRoom({ ...pi, room: 'desktop', node_kind: 'desktop' })).toBeNull()
  })

  it('tells apart two nodes that would read the same', () => {
    const n = (node_id: string, room: string) => ({ node_id, room, node_kind: null }) as NodeLiveness
    const names = nodeNames([n('aaaa1111-x', 'kitchen'), n('bbbb2222-y', 'kitchen'), n('cccc3333-z', 'office')])
    expect([...names.values()]).toEqual(['kitchen (aaaa1111)', 'kitchen (bbbb2222)', 'office'])
  })
})
