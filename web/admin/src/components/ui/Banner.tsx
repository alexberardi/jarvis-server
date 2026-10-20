import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export type BannerTone = 'neutral' | 'primary' | 'success' | 'warning' | 'danger'

interface BannerProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  tone?: BannerTone
  /** A lucide icon, drawn in the tone's colour. */
  icon?: ReactNode
  /** The one line that says what is going on. */
  title?: ReactNode
  /** The actions at the right (under the text on a phone): usually one Button or button-styled link. */
  actions?: ReactNode
}

// The surface is the tone's soft background; text stays the page's text colour (AA on every soft tone).
const tones: Record<BannerTone, string> = {
  neutral: 'border-border bg-surface-alt',
  primary: 'border-primary/30 bg-primary-soft',
  success: 'border-success/30 bg-success-soft',
  warning: 'border-warning/30 bg-warning-soft',
  danger: 'border-danger/40 bg-danger-soft',
}

const iconTones: Record<BannerTone, string> = {
  neutral: 'text-text-muted',
  primary: 'text-primary',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

/** Banner is a page-wide notice above the content: something that needs the user, or is under way. */
export default function Banner({ tone = 'neutral', icon, title, actions, className, children, ...rest }: BannerProps) {
  return (
    <div className={cn('flex flex-wrap items-center gap-x-4 gap-y-3 rounded-card border p-4', tones[tone], className)} {...rest}>
      <div className="flex min-w-0 flex-1 basis-60 items-start gap-3">
        {icon && <span aria-hidden className={cn('mt-0.5 flex shrink-0', iconTones[tone])}>{icon}</span>}
        <div className="min-w-0 space-y-0.5">
          {title && <p className="text-subheading text-text">{title}</p>}
          {children && <div className="text-small text-text">{children}</div>}
        </div>
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}
