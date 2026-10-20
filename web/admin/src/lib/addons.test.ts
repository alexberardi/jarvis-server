/// <reference types="node" />
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import type { NodeLiveness } from '@/api/nodes'
import { canSignOut, detectOS, extensionLabel, WEBSITE_ACCESS, websiteAccessFamilies, yourDevices } from './addons'

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

describe('canSignOut (decision 2026-10-14)', () => {
  const n = (owner: number | null, hh = 'h1') => ({ ...base, household_id: hh, owner_user_id: owner })
  const base = { node_id: 'a', room: 'office', online: true, last_seen: null, last_seen_version: null, is_busy: false }
  it('is the pairer or a household admin, never another household', () => {
    expect(canSignOut(n(7), 7, [{ id: 'h1', role: 'member' }])).toBe(true)
    expect(canSignOut(n(8), 7, [{ id: 'h1', role: 'member' }])).toBe(false)
    expect(canSignOut(n(8), 7, [{ id: 'h1', role: 'power_user' }])).toBe(false)
    expect(canSignOut(n(null), 7, [{ id: 'h1', role: 'admin' }])).toBe(true)
    expect(canSignOut(n(null), 7, [{ id: 'h1', role: 'owner' }])).toBe(true)
    expect(canSignOut(n(7, 'h2'), 7, [{ id: 'h1', role: 'admin' }])).toBe(false)
  })
})

describe('website access wording', () => {
  // One wording for the Add-ons page and the store listing (both stores paste the same Full
  // description). Outside the SPA's root, so read from disk; vitest runs in web/admin.
  const listing = readFileSync(path.resolve(process.cwd(), '../../internal/agent/browser/store/listing.md'), 'utf8')
  const description = listing.split('## Full description (both stores)')[1].split('```')[1]

  it('is the store listing\'s, word for word', () => {
    for (const s of [WEBSITE_ACCESS.why, WEBSITE_ACCESS.firefox, WEBSITE_ACCESS.chrome, WEBSITE_ACCESS.reload, WEBSITE_ACCESS.docs]) {
      expect(description).toContain(s)
    }
  })

  it('picks the step for the browser that has it off, both when it cannot tell', () => {
    expect(websiteAccessFamilies('Firefox')).toEqual(['firefox'])
    expect(websiteAccessFamilies('LibreWolf')).toEqual(['firefox'])
    expect(websiteAccessFamilies('Google Chrome')).toEqual(['chrome'])
    expect(websiteAccessFamilies('Microsoft Edge')).toEqual(['chrome'])
    expect(websiteAccessFamilies('Brave, Firefox')).toEqual(['firefox', 'chrome'])
    expect(websiteAccessFamilies('unknown')).toEqual(['firefox', 'chrome'])
    expect(websiteAccessFamilies(undefined)).toEqual(['firefox', 'chrome'])
    expect(websiteAccessFamilies('')).toEqual(['firefox', 'chrome'])
  })
})
