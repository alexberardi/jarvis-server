import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface EmptyStateProps {
  /** A lucide icon, drawn muted. */
  icon?: ReactNode
  title: ReactNode
  /** The title's element: a heading when the empty state is the whole page (h1) or a section (h2). */
  titleAs?: 'p' | 'h1' | 'h2'
  /** What the list will hold, or how to fill it. */
  description?: ReactNode
  /** The one thing to do next, usually a Button. */
  action?: ReactNode
  className?: string
}

/** EmptyState says what goes here and how to add it, in place of an empty list. */
export default function EmptyState({ icon, title, titleAs: Title = 'p', description, action, className }: EmptyStateProps) {
  return (
    <div className={cn('flex flex-col items-center px-4 py-10 text-center', className)}>
      {icon && (
        <span aria-hidden className="mb-3 flex size-10 items-center justify-center rounded-pill bg-surface-alt text-text-muted">
          {icon}
        </span>
      )}
      <Title className={cn(Title === 'p' ? 'text-subheading' : 'text-heading', 'text-text')}>{title}</Title>
      {description && <p className="mt-1 max-w-sm text-small text-text-muted">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
