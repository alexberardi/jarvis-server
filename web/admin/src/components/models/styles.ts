/** Shared class names and tones for the Models components (apart from them for fast refresh). */

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'muted'

export const toneClass: Record<Tone, string> = {
  // The status tokens on their soft fills: the pairs tokens.test.ts checks for AA (audit 2026-10 #1).
  ok: 'bg-success-soft text-success',
  warn: 'bg-warning-soft text-warning',
  bad: 'bg-danger-soft text-danger',
  info: 'bg-primary-soft text-primary',
  muted: 'bg-surface-alt text-text-muted',
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
    'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
  secondary:
    'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50',
  danger:
    'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border border-red-500/40 px-3 py-1.5 text-xs text-red-500 hover:bg-red-500/10 disabled:cursor-not-allowed disabled:opacity-50',
  dangerSolid:
    'inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg bg-red-600 px-3 py-1.5 text-xs font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
  /** A text link used as a card action ("Manage", "All nodes"); tall enough to tap below md. */
  link: 'inline-flex items-center text-xs text-[var(--color-primary)] hover:underline max-md:min-h-10',
  icon: 'inline-flex items-center justify-center rounded-lg p-1.5 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)] disabled:opacity-50',
}

/**
 * Modal dialogs: a bottom sheet on phones (full width, up to the full viewport height, scrolls
 * inside), a centred card from sm up. Panel takes extra spacing classes (space-y-*).
 */
export const dialogClass = {
  backdrop: 'fixed inset-0 z-50 flex items-end justify-center bg-black/50 sm:items-center sm:p-4',
  panel:
    'max-h-dvh w-full overflow-y-auto rounded-t-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5 pb-[max(1.25rem,env(safe-area-inset-bottom))] shadow-xl sm:max-h-[calc(100dvh-2rem)] sm:max-w-lg sm:rounded-xl sm:pb-5',
}

export const inputClass =
  'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-1.5 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]'

