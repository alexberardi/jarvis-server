import { describe, expect, it } from 'vitest'
import { DEFAULT_RELAY_URL, relayChange } from './privacy'

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
