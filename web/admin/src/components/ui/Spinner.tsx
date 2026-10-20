import { cn } from '@/lib/utils'

interface SpinnerProps {
  className?: string
  /** Read out instead of hiding the spinner; leave it unset inside a labelled control. */
  label?: string
}

/** Spinner is a small ring in the current text colour. Reduced motion slows it to 1.6s (index.css). */
export default function Spinner({ className, label }: SpinnerProps) {
  return (
    <span
      role={label ? 'status' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className={cn('inline-block size-4 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent', className)}
    />
  )
}
