import { describe, expect, it } from 'vitest'
import { formatServerTime, parseServerTime, timeAgo } from './time'

describe('parseServerTime', () => {
  it('reads a naive timestamp (cc legacy shape) as UTC, not local time', () => {
    expect(parseServerTime('2026-10-13T02:55:12.175000')?.toISOString()).toBe('2026-10-13T02:55:12.175Z')
    expect(parseServerTime('2026-10-13T02:55:12')?.toISOString()).toBe('2026-10-13T02:55:12.000Z')
    expect(parseServerTime('2026-10-13 02:55:12')?.toISOString()).toBe('2026-10-13T02:55:12.000Z')
  })

  it('keeps an explicit zone', () => {
    expect(parseServerTime('2026-10-13T02:55:12.500000Z')?.toISOString()).toBe('2026-10-13T02:55:12.500Z')
    expect(parseServerTime('2026-10-12T22:55:12-04:00')?.toISOString()).toBe('2026-10-13T02:55:12.000Z')
    expect(parseServerTime('2026-10-13T02:55:12+00:00')?.toISOString()).toBe('2026-10-13T02:55:12.000Z')
  })

  it('handles empty and garbage', () => {
    expect(parseServerTime(null)).toBeNull()
    expect(parseServerTime('')).toBeNull()
    expect(parseServerTime('soon')).toBeNull()
  })
})

describe('timeAgo', () => {
  const now = Date.parse('2026-10-13T03:13:00Z')

  it('counts from UTC for a naive last_seen: 18 minutes, not "just now"', () => {
    expect(timeAgo('2026-10-13T02:55:00.000000', now)).toBe('18 min ago')
    expect(timeAgo('2026-10-13T02:55:00Z', now)).toBe('18 min ago')
  })

  it('buckets', () => {
    expect(timeAgo('2026-10-13T03:12:30Z', now)).toBe('just now')
    expect(timeAgo('2026-10-13T03:14:00Z', now)).toBe('just now') // clock skew
    expect(timeAgo('2026-10-13T00:13:00Z', now)).toBe('3 h ago')
    expect(timeAgo('2026-10-11T03:13:00Z', now)).toBe('2 d ago')
    expect(timeAgo('2026-09-01T03:13:00Z', now)).toBe(new Date('2026-09-01T03:13:00Z').toLocaleDateString())
    expect(timeAgo(null, now)).toBe('never')
    expect(timeAgo('soon', now)).toBe('soon')
  })
})

describe('formatServerTime', () => {
  it('renders naive UTC in local time', () => {
    expect(formatServerTime('2026-10-13T02:55:12')).toBe(new Date('2026-10-13T02:55:12Z').toLocaleString())
    expect(formatServerTime('soon')).toBe('soon')
    expect(formatServerTime(null)).toBe('')
  })
})
