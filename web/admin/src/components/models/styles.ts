/** Shared class names and tones for the Models components (apart from them for fast refresh). */

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'muted'

export const toneClass: Record<Tone, string> = {
  ok: 'bg-green-500/10 text-green-500',
  warn: 'bg-amber-500/10 text-amber-500',
  bad: 'bg-red-500/10 text-red-500',
  info: 'bg-[var(--color-primary)]/10 text-[var(--color-primary)]',
  muted: 'bg-[var(--color-surface-alt)] text-[var(--color-text-muted)]',
}

/** stateTone colours an engine/label/install state. */
export function stateTone(state: string): Tone {
  switch (state) {
    case 'ready':
    case 'healthy':
    case 'running':
    case 'done':
    case 'remote':
      return 'ok'
    case 'degraded':
    case 'starting':
    case 'restarting':
    case 'draining':
    case 'fetching_engine':
    case 'queued':
    case 'downloading':
    case 'installing':
      return 'warn'
    case 'failed':
    case 'misconfigured':
    case 'no_engine_build':
    case 'gpu_unavailable':
    case 'stopped':
    case 'cancelled':
      return 'bad'
    default:
      return 'muted'
  }
}

export const buttonClass = {
  primary:
    'inline-flex items-center justify-center gap-1.5 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
  secondary:
    'inline-flex items-center justify-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50',
  danger:
    'inline-flex items-center justify-center gap-1.5 rounded-lg border border-red-500/40 px-3 py-1.5 text-xs text-red-500 hover:bg-red-500/10 disabled:cursor-not-allowed disabled:opacity-50',
  dangerSolid:
    'inline-flex items-center justify-center gap-1.5 rounded-lg bg-red-600 px-3 py-1.5 text-xs font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
  icon: 'rounded-lg p-1.5 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)] disabled:opacity-50',
}

export const inputClass =
  'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-1.5 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]'

