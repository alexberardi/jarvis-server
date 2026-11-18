import { describe, expect, it } from 'vitest'
import { fromLocalInput, toLocalInput } from './recordFields'

describe('record datetimes', () => {
  it('shows a zoned value in local time and a naive one as written', () => {
    const d = new Date('2026-11-18T12:30:00Z')
    const local = `${d.getFullYear()}-11-${String(d.getDate()).padStart(2, '0')}T${String(d.getHours()).padStart(2, '0')}:30`
    expect(toLocalInput('2026-11-18T12:30:00+00:00')).toBe(local)
    expect(toLocalInput('2026-11-18T07:45:12')).toBe('2026-11-18T07:45')
    expect(toLocalInput(null)).toBe('')
  })

  it('sends a picked time as mobile does: local, with seconds, no zone', () => {
    expect(fromLocalInput('2026-11-18T07:45')).toBe('2026-11-18T07:45:00')
    expect(fromLocalInput('')).toBeNull()
  })
})
