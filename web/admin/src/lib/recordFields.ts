import type { FieldSpec } from '@/api/commandData'

// The types a form can edit; anything else (lists, objects, user refs) shows read-only.
// shortcut: lists and objects aren't editable on the web, upgrade when a command needs it.
const EDITABLE = new Set(['string', 'text', 'id', 'int', 'float', 'bool', 'enum', 'date', 'time', 'datetime'])

export const canEdit = (f: FieldSpec) => EDITABLE.has(f.type) && !(f.type === 'enum' && !f.enum_values?.length)

const pad = (n: number) => String(n).padStart(2, '0')

/**
 * toLocalInput is a stored datetime as a datetime-local value ("2026-11-18T07:30"): a value with a
 * zone in this browser's local time, a value without one as written (the node's local time).
 */
export function toLocalInput(v: unknown): string {
  if (typeof v !== 'string' || !v) return ''
  if (!/(Z|[+-]\d\d:?\d\d)$/.test(v)) return v.slice(0, 16)
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v.slice(0, 16)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** fromLocalInput is what mobile sends for a picked time: local, with seconds, no zone. */
export const fromLocalInput = (v: string): string | null => (v ? `${v.slice(0, 16)}:00` : null)
