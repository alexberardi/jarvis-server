import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface CardProps extends HTMLAttributes<HTMLElement> {
  /** `section` when the card has a heading of its own (it is then a labelled region). */
  as?: 'div' | 'section' | 'article'
  /** Drop the padding, for a card whose content is a List or Table that runs edge to edge. */
  flush?: boolean
}

/** Card is a surface with a border (B uses borders, not shadows): 12px radius, 16px padding (20 from md). */
export default function Card({ as: Tag = 'div', flush, className, ...rest }: CardProps) {
  return (
    <Tag
      className={cn('min-w-0 rounded-card border border-border bg-surface', !flush && 'p-4 md:p-5', className)}
      {...rest}
    />
  )
}

interface CardHeaderProps {
  title: ReactNode
  description?: ReactNode
  /** Buttons at the right of the heading. */
  actions?: ReactNode
  /** The heading's id, for a section's aria-labelledby. */
  id?: string
  className?: string
}

/** CardHeader is a card's heading row: an h2, an optional line under it, and actions. */
export function CardHeader({ title, description, actions, id, className }: CardHeaderProps) {
  return (
    <div className={cn('mb-4 flex flex-wrap items-start justify-between gap-3', className)}>
      <div className="min-w-0">
        <h2 id={id} className="text-heading text-text">
          {title}
        </h2>
        {description && <p className="mt-0.5 text-small text-text-muted">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
