import { describe, expect, it } from 'vitest'
import { formatVersion } from './format'

describe('formatVersion', () => {
  it('prefixes semver-looking versions with v', () => {
    expect(formatVersion('0.3.2')).toBe('v0.3.2')
    expect(formatVersion('1.2')).toBe('v1.2')
    expect(formatVersion('0.1.0-rc14')).toBe('v0.1.0-rc14')
    expect(formatVersion('1.0.0+build.5')).toBe('v1.0.0+build.5')
  })

  it('leaves dev builds and already-prefixed versions alone', () => {
    expect(formatVersion('dev-5b3667a')).toBe('dev-5b3667a')
    expect(formatVersion('v0.3.2')).toBe('v0.3.2')
    expect(formatVersion('5b3667a')).toBe('5b3667a')
    expect(formatVersion('2026.10.12')).toBe('v2026.10.12')
    expect(formatVersion('')).toBe('')
  })
})
