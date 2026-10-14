import { describe, expect, it } from 'vitest'
import type { NodeLiveness } from '@/api/nodes'
import { detectOS, extensionLabel, yourDevices } from './addons'

const n = (node_id: string, hh: string | null, online: boolean, node_kind: string | null = 'desktop', room = node_id): NodeLiveness => ({
  node_id,
  room,
  household_id: hh,
  online,
  last_seen: null,
  last_seen_version: null,
  is_busy: false,
  node_kind,
})

describe('detectOS', () => {
  it.each([
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit', 'MacIntel', 'macos'],
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64)', 'Win32', 'windows'],
    ['Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0', 'Linux x86_64', 'linux'],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)', 'iPhone', 'other'],
    ['Mozilla/5.0 (Linux; Android 15; Pixel 9)', 'Linux armv8l', 'other'],
  ])('%s', (ua, platform, want) => expect(detectOS(ua, platform)).toBe(want))
})

describe('yourDevices (install plan §3.2 "Your devices")', () => {
  it("keeps desktop agents of the user's own households, online first", () => {
    const got = yourDevices(
      [
        n('pi', 'h1', true, null),
        n('laptop', 'h1', false),
        n('office', 'h2', true),
        n('foreign', 'h9', true),
        n('orphan', null, true),
      ],
      ['h1', 'h2'],
    )
    expect(got.map((d) => d.node_id)).toEqual(['office', 'laptop'])
  })
})

describe('extensionLabel', () => {
  it('reads the heartbeat state (install plan 1.8)', () => {
    expect(extensionLabel('connected')).toBe('Browser extension connected')
    expect(extensionLabel('disconnected')).toBe('Browser extension not connected')
    expect(extensionLabel('disabled')).toBe('Browser control turned off')
    expect(extensionLabel(null)).toBe('Browser extension: not reported')
  })
})
