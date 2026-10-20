import type { ReactNode } from 'react'

export interface FieldProps {
  /** The visible label. */
  label?: ReactNode
  /** A line under the field. */
  hint?: ReactNode
  /** Replaces the hint and marks the field invalid. */
  error?: ReactNode
}

/** fieldControl is the shared look of a text field or select: B's border-strong edge, 40px tall. */
export const fieldControl =
  'h-10 w-full min-w-0 rounded-control border border-border-strong bg-surface px-3 text-body text-text ' +
  'placeholder:text-text-muted transition-[border-color,background-color] duration-(--duration-hover) ' +
  'disabled:cursor-not-allowed disabled:bg-surface-alt disabled:opacity-60 ' +
  'aria-invalid:border-danger'

/** noteProps gives the control its describedby and invalid state. */
export function noteProps(id: string, { hint, error }: FieldProps) {
  return {
    'aria-describedby': error || hint ? `${id}-note` : undefined,
    'aria-invalid': error ? true : undefined,
  }
}
