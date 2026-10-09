import { describe, expect, it } from 'vitest'
import type { ServiceSettingsResult } from '@/types/settings'
import { DEFAULT_RELAY_URL, PRIVACY_TOGGLES, privacyChanges, readPrivacy, relayChange } from './privacy'

describe('relayChange', () => {
  it('turns push on with the project relay', () => {
    expect(relayChange('', true)).toEqual({ service: 'notifications', key: 'relay.url', value: DEFAULT_RELAY_URL })
  })
  it('keeps a configured relay when already on', () => {
    expect(relayChange('https://relay.example.lan', true)).toBeNull()
  })
  it('clears the relay when turned off', () => {
    expect(relayChange('https://relay.example.lan', false)).toEqual({ service: 'notifications', key: 'relay.url', value: '' })
  })
  it('writes nothing when off stays off', () => {
    expect(relayChange('', false)).toBeNull()
  })
})

describe('Pantry privacy toggle', () => {
  const ccSettings = (value: unknown): ServiceSettingsResult[] =>
    [{ service_name: 'cc', success: true, error: null, latency_ms: 1, settings: [{ key: 'pantry.enabled', value }] }] as unknown as ServiceSettingsResult[]

  it('is an off-box cc toggle', () => {
    const t = PRIVACY_TOGGLES.find((x) => x.key === 'pantry.enabled')
    expect(t).toMatchObject({ service: 'cc', title: 'Pantry package store', offBox: true })
    expect(t?.what).toMatch(/Pantry/)
    expect(t?.what).toMatch(/IP address/)
  })

  it('reads the current value and writes only a change', () => {
    const current = readPrivacy(ccSettings(false))
    expect(current['cc/pantry.enabled']).toBe(false)
    expect(privacyChanges(current, { ...current })).toEqual([])
    expect(privacyChanges(current, { ...current, 'cc/pantry.enabled': true })).toEqual([
      { service: 'cc', key: 'pantry.enabled', value: true },
    ])
    expect(readPrivacy(ccSettings('true'))['cc/pantry.enabled']).toBe(true)
  })

  it('is left out when this jarvisd does not define it', () => {
    expect('cc/pantry.enabled' in readPrivacy([])).toBe(false)
  })
})
