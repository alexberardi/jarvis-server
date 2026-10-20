import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import type { FieldProps } from './fieldStyles'

interface FieldFrameProps extends FieldProps {
  id: string
  className?: string
  children: ReactNode
}

/** FieldFrame lays out a label, the control and its hint or error, wired up by id. */
export function FieldFrame({ id, label, hint, error, className, children }: FieldFrameProps) {
  const note = error ?? hint
  return (
    <div className={cn('flex min-w-0 flex-col gap-1.5', className)}>
      {label && (
        <label htmlFor={id} className="text-small font-medium text-text">
          {label}
        </label>
      )}
      {children}
      {note && (
        <p id={`${id}-note`} className={cn('text-small', error ? 'text-danger' : 'text-text-muted')}>
          {note}
        </p>
      )}
    </div>
  )
}
