import type { HTMLAttributes, ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'

interface ListProps extends HTMLAttributes<HTMLUListElement> {
  /** Names the list for screen readers when nothing visible does. */
  label?: string
}

/** List is a column of rows divided by hairlines. Put it in a flush Card for a bordered list. */
export default function List({ label, className, ...rest }: ListProps) {
  return <ul aria-label={label} className={cn('divide-y divide-border', className)} {...rest} />
}

interface ListItemProps {
  title: ReactNode
  /** The line under the title (meta, a time, a state). */
  description?: ReactNode
  /** Before the text: an icon or avatar. */
  leading?: ReactNode
  /** After the text: a Badge, a value or a button. */
  trailing?: ReactNode
  /** Makes the whole row a link (with a chevron). */
  to?: string
  /** Makes the whole row a button. Ignored when `to` is set. */
  onClick?: () => void
  className?: string
}

/** ListItem is a 44px row (B's density): leading, title and description, trailing. */
export function ListItem({ title, description, leading, trailing, to, onClick, className }: ListItemProps) {
  const body = (
    <>
      {leading && <span className="flex shrink-0 items-center text-text-muted">{leading}</span>}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-subheading text-text">{title}</span>
        {description && <span className="block truncate text-small text-text-muted">{description}</span>}
      </span>
      {trailing && <span className="flex shrink-0 items-center gap-2">{trailing}</span>}
      {to && <ChevronRight size={16} aria-hidden className="shrink-0 text-text-muted" />}
    </>
  )
  const row = 'flex min-h-11 w-full items-center gap-3 px-4 py-2 text-left'
  const interactive = 'transition-colors duration-(--duration-hover) hover:bg-surface-alt'
  return (
    <li className={className}>
      {to ? (
        <Link to={to} className={cn(row, interactive)}>
          {body}
        </Link>
      ) : onClick ? (
        <button type="button" onClick={onClick} className={cn(row, interactive)}>
          {body}
        </button>
      ) : (
        <div className={row}>{body}</div>
      )}
    </li>
  )
}
