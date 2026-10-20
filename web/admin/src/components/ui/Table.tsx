import type { HTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

interface TableProps extends HTMLAttributes<HTMLTableElement> {
  /** The table's caption, for screen readers; set `captionVisible` to show it. */
  caption?: string
  captionVisible?: boolean
}

/**
 * Table is a data table that scrolls sideways inside its own box on a narrow screen, so the
 * page never does. Rows are 44px. On a phone, prefer a List when the rows are few columns.
 */
export default function Table({ caption, captionVisible, className, children, ...rest }: TableProps) {
  return (
    <div className="w-full min-w-0 overflow-x-auto">
      <table className={cn('w-full border-collapse text-left text-body', className)} {...rest}>
        {caption && (
          <caption className={cn('pb-2 text-left text-small text-text-muted', !captionVisible && 'sr-only')}>
            {caption}
          </caption>
        )}
        {children}
      </table>
    </div>
  )
}

export function THead({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn('border-b border-border', className)} {...rest} />
}

export function TBody({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn('divide-y divide-border', className)} {...rest} />
}

export function TR({ className, ...rest }: HTMLAttributes<HTMLTableRowElement>) {
  return <tr className={className} {...rest} />
}

/** TH is a column header (scope=col) unless told otherwise. */
export function TH({ className, scope = 'col', ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      scope={scope}
      className={cn('h-10 px-4 text-caption font-medium whitespace-nowrap text-text-muted', className)}
      {...rest}
    />
  )
}

export function TD({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn('h-11 px-4 py-2 text-text', className)} {...rest} />
}
