/**
 * parseServerTime reads a server timestamp. cc's legacy-shaped routes (the node list among
 * them) send naive UTC with no zone, "2026-10-13T02:55:12.175000", which `new Date` would read
 * as local time: a node seen 18 minutes ago looked hours in the future ("just now"), a trace
 * from 10:55 PM showed as 02:55 AM. A date-time without a zone is UTC here. Null when empty or
 * unparsable.
 */
export function parseServerTime(iso: string | null | undefined): Date | null {
  if (!iso) return null
  let s = iso.trim().replace(/^(\d{4}-\d\d-\d\d) (\d)/, '$1T$2')
  // Milliseconds at most: engines differ on longer fractions.
  s = s.replace(/(\.\d{3})\d+/, '$1')
  if (/T\d\d:\d\d/.test(s) && !/(?:[zZ]|[+-]\d\d:?\d\d)$/.test(s)) s += 'Z'
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? null : d
}

/**
 * timeAgo renders how long ago a server timestamp was: "just now", "18 min ago", "3 h ago",
 * "2 d ago", then the date. "never" for none; the raw string when unparsable. A time slightly
 * in the future (clock skew) is "just now".
 */
export function timeAgo(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return 'never'
  const t = parseServerTime(iso)
  if (!t) return iso
  const s = Math.max(0, (now - t.getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  if (s < 7 * 86400) return `${Math.floor(s / 86400)} d ago`
  return t.toLocaleDateString()
}

/** formatServerTime renders a server timestamp in local time; the raw string when unparsable. */
export function formatServerTime(iso: string | null | undefined, opts?: Intl.DateTimeFormatOptions): string {
  if (!iso) return ''
  const t = parseServerTime(iso)
  return t ? t.toLocaleString(undefined, opts) : iso
}
