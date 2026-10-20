import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export type BadgeTone = 'neutral' | 'primary' | 'success' | 'warning' | 'danger'

interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone
  /** A dot before the label, for a live state (online, running). */
  dot?: boolean
}

// Each tone is its colour on its soft background, which tokens.test.ts checks for AA.
const tones: Record<BadgeTone, string> = {
  neutral: 'bg-surface-alt text-text-muted',
  primary: 'bg-primary-soft text-primary',
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
  danger: 'bg-danger-soft text-danger',
}

/** Badge is a small status chip, 12px (the type scale's floor). */
export default function Badge({ tone = 'neutral', dot, className, children, ...rest }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex h-6 shrink-0 items-center gap-1.5 rounded-pill px-2 text-caption whitespace-nowrap',
        tones[tone],
        className,
      )}
      {...rest}
    >
      {dot && <span aria-hidden className="size-1.5 rounded-pill bg-current" />}
      {children}
    </span>
  )
}
