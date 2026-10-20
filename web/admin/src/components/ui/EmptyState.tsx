import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface EmptyStateProps {
  /** A lucide icon, drawn muted. */
  icon?: ReactNode
  title: ReactNode
  /** What the list will hold, or how to fill it. */
  description?: ReactNode
  /** The one thing to do next, usually a Button. */
  action?: ReactNode
  className?: string
}

/** EmptyState says what goes here and how to add it, in place of an empty list. */
export default function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cn('flex flex-col items-center px-4 py-10 text-center', className)}>
      {icon && (
        <span aria-hidden className="mb-3 flex size-10 items-center justify-center rounded-pill bg-surface-alt text-text-muted">
          {icon}
        </span>
      )}
      <p className="text-subheading text-text">{title}</p>
      {description && <p className="mt-1 max-w-sm text-small text-text-muted">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
