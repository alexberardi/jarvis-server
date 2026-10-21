import type { ReactNode } from 'react'
import { Spinner } from '@/components/ui'
import { cn } from '@/lib/utils'

/** StepHeader is a wizard step's h2 (with its icon and an optional badge) and the line under it. */
export function StepHeader({
  icon,
  title,
  badge,
  actions,
  children,
}: {
  icon?: ReactNode
  title: ReactNode
  badge?: ReactNode
  /** A small action at the right of the heading ("Run again"). */
  actions?: ReactNode
  /** What the step is for, in a sentence or two. */
  children?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0 flex-1 basis-64">
        <h2 className="flex items-start gap-2 text-title text-balance text-text">
          {icon && (
            <span aria-hidden className="flex h-[1.25em] shrink-0 items-center text-text-muted">
              {icon}
            </span>
          )}
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            {title}
            {badge}
          </span>
        </h2>
        {children && <p className="mt-1.5 max-w-prose text-body text-pretty text-text-muted">{children}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}

/** StepActions is a step's footer: Back (or nothing) at the left, the way on at the right. */
export function StepActions({ back, children, className }: { back?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4', className)}>
      <div>{back}</div>
      <div className="flex flex-wrap items-center justify-end gap-2">{children}</div>
    </div>
  )
}

/** StepLoading says what a step is waiting for, with a spinner, in place of its content. */
export function StepLoading({ children }: { children: string }) {
  return (
    <p className="flex items-center gap-2 text-small text-text-muted">
      <Spinner label={children} />
      <span aria-hidden>{children}</span>
    </p>
  )
}
