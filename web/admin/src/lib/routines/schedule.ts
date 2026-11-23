import type { RoutineSchedule } from '@/api/routines'

/** Days in the editor's order, with cron's day numbers (Sunday 0). */
export const DAYS = [
  { key: 'mon', label: 'Mon', cron: 1 },
  { key: 'tue', label: 'Tue', cron: 2 },
  { key: 'wed', label: 'Wed', cron: 3 },
  { key: 'thu', label: 'Thu', cron: 4 },
  { key: 'fri', label: 'Fri', cron: 5 },
  { key: 'sat', label: 'Sat', cron: 6 },
  { key: 'sun', label: 'Sun', cron: 0 },
] as const
export type Day = (typeof DAYS)[number]['key']
export const ALL_DAYS: Day[] = DAYS.map((d) => d.key)
export const WEEKDAYS: Day[] = ['mon', 'tue', 'wed', 'thu', 'fri']
export const WEEKENDS: Day[] = ['sat', 'sun']

/** Interval choices in minutes (mobile's INTERVAL_PRESETS). */
export const INTERVAL_PRESETS = [15, 30, 60, 120, 240, 360, 720]

/** buildCron is the 5-field cron for a local "HH:MM" on days (every day is "*"). */
export function buildCron(time: string, days: Day[]): string {
  const [h, m] = time.split(':').map((n) => parseInt(n, 10))
  const field =
    days.length === 0 || days.length === 7
      ? '*'
      : DAYS.filter((d) => days.includes(d.key))
          .map((d) => d.cron)
          .sort((a, b) => a - b)
          .join(',')
  return `${Number.isNaN(m) ? 0 : m} ${Number.isNaN(h) ? 8 : h} * * ${field}`
}

/** parseCron reads buildCron's shape back: "HH:MM" and the days ("*" is every day). */
export function parseCron(cron: string | null | undefined): { time: string; days: Day[] } {
  const parts = (cron ?? '').trim().split(/\s+/)
  if (parts.length < 5) return { time: '08:00', days: [...ALL_DAYS] }
  const m = parseInt(parts[0], 10)
  const h = parseInt(parts[1], 10)
  const time = `${String(Number.isNaN(h) ? 8 : h).padStart(2, '0')}:${String(Number.isNaN(m) ? 0 : m).padStart(2, '0')}`
  if (parts[4] === '*') return { time, days: [...ALL_DAYS] }
  const nums = new Set(parts[4].split(',').map((n) => parseInt(n, 10) % 7))
  return { time, days: DAYS.filter((d) => nums.has(d.cron)).map((d) => d.key) }
}

export function minutesLabel(mins: number): string {
  if (mins >= 1440 && mins % 1440 === 0) return `${mins / 1440} d`
  if (mins >= 60 && mins % 60 === 0) return `${mins / 60} h`
  return `${mins} min`
}

/** daysLabel is "Every day", "Weekdays", "Weekends" or "Mon, Thu". */
export function daysLabel(days: Day[]): string {
  if (days.length === 7 || days.length === 0) return 'Every day'
  if (days.length === 5 && WEEKDAYS.every((d) => days.includes(d))) return 'Weekdays'
  if (days.length === 2 && WEEKENDS.every((d) => days.includes(d))) return 'Weekends'
  return DAYS.filter((d) => days.includes(d.key))
    .map((d) => d.label)
    .join(', ')
}

function timeLabel(time: string): string {
  const [h, m] = time.split(':').map((n) => parseInt(n, 10))
  return new Date(2000, 0, 1, h, m).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
}

/** scheduleLabel is a schedule in words for the list: "Weekdays · 7:00 AM", "Every 2 h". */
export function scheduleLabel(s: RoutineSchedule | null | undefined): string | null {
  if (!s) return null
  if (s.type === 'interval') return `Every ${minutesLabel(Math.round((s.interval_seconds ?? 0) / 60))}`
  const { time, days } = parseCron(s.cron)
  return `${daysLabel(days)} · ${timeLabel(time)}`
}
