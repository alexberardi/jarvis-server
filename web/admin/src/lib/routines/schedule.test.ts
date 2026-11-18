import { describe, expect, it } from 'vitest'
import { buildCron, parseCron, scheduleLabel } from './schedule'

describe('routine schedules', () => {
  it('builds and reads back cron for days and a time', () => {
    expect(buildCron('07:30', ['mon', 'tue', 'wed', 'thu', 'fri'])).toBe('30 7 * * 1,2,3,4,5')
    expect(buildCron('18:00', ['sun', 'sat'])).toBe('0 18 * * 0,6')
    expect(buildCron('08:00', ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'])).toBe('0 8 * * *')
    expect(parseCron('30 7 * * 1,2,3,4,5')).toEqual({ time: '07:30', days: ['mon', 'tue', 'wed', 'thu', 'fri'] })
    expect(parseCron('0 18 * * 0,6')).toEqual({ time: '18:00', days: ['sat', 'sun'] })
    expect(parseCron('0 8 * * *').days).toHaveLength(7)
    expect(parseCron(null)).toEqual({ time: '08:00', days: ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] })
  })

  it('says a schedule in words', () => {
    expect(scheduleLabel(null)).toBeNull()
    expect(scheduleLabel({ type: 'interval', interval_seconds: 7200, timezone: 'UTC', enabled: true })).toBe('Every 2 h')
    expect(scheduleLabel({ type: 'cron', cron: '0 7 * * 1,2,3,4,5', timezone: 'UTC', enabled: true })).toMatch(/^Weekdays · 7:00/)
    expect(scheduleLabel({ type: 'cron', cron: '0 9 * * 1,4', timezone: 'UTC', enabled: true })).toMatch(/^Mon, Thu · 9:00/)
  })
})
