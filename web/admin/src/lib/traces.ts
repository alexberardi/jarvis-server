/** formatDuration is a trace or span's length: whole milliseconds under a second, then seconds. */
export function formatDuration(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

/** sourceLabel names where a request came from. */
export function sourceLabel(source: string): string {
  return source === 'mobile' ? 'Mobile' : 'Node'
}
