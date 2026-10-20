import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  /** The page's main actions, at the right (under the title on a phone). */
  actions?: ReactNode
  className?: string
}

/** PageHeader is the top of a page: its one h1, a line under it and its actions. */
export default function PageHeader({ title, description, actions, className }: PageHeaderProps) {
  return (
    <header className={cn('mb-6 flex flex-wrap items-end justify-between gap-x-4 gap-y-3', className)}>
      <div className="min-w-0">
        <h1 className="text-title text-text">{title}</h1>
        {description && <p className="mt-1 max-w-prose text-body text-text-muted">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  )
}
