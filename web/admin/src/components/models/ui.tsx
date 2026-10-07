import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { toneClass, type Tone } from './styles'

/** Section is a titled card; the Models page and the A7 wizard steps share it. */
export function Section({
  title,
  icon: Icon,
  description,
  actions,
  children,
  className,
}: {
  title: string
  icon?: LucideIcon
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section
      className={cn('rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4', className)}
      aria-label={title}
    >
      <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
            {Icon && <Icon size={16} className="text-[var(--color-text-muted)]" />}
            {title}
          </h2>
          {description && <div className="mt-0.5 text-xs text-[var(--color-text-muted)]">{description}</div>}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  )
}

export function Pill({ tone = 'muted', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span title={title} className={cn('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium', toneClass[tone])}>
      {children}
    </span>
  )
}

export function ProgressBar({ value, max, label }: { value: number; max: number; label?: string }) {
  const pct = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      className="h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-surface-alt)]"
    >
      <div className="h-full rounded-full bg-[var(--color-primary)] transition-[width]" style={{ width: `${pct}%` }} />
    </div>
  )
}
