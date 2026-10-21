import type { ReactNode } from 'react'
import { AlertCircle, AlertTriangle, CheckCircle2, Info } from 'lucide-react'
import { cn } from '@/lib/utils'

export type AlertTone = 'info' | 'success' | 'warning' | 'danger'

// Each tone is its colour on its soft background, the pairs tokens.test.ts checks for AA.
const tones: Record<AlertTone, { box: string; icon: ReactNode }> = {
  info: { box: 'bg-surface-alt text-text', icon: <Info size={16} aria-hidden className="text-text-muted" /> },
  success: { box: 'bg-success-soft text-success', icon: <CheckCircle2 size={16} aria-hidden /> },
  warning: { box: 'bg-warning-soft text-warning', icon: <AlertTriangle size={16} aria-hidden /> },
  danger: { box: 'bg-danger-soft text-danger', icon: <AlertCircle size={16} aria-hidden /> },
}

interface AlertProps {
  tone?: AlertTone
  /** What happened, in a few words. */
  title?: ReactNode
  /** What it means, or what to do next. */
  children?: ReactNode
  className?: string
}

/**
 * Alert is a message inside a page or form: a form's error (danger, read out at once), a warning
 * that doesn't block, a success or a note. A field's own error goes on the field instead
 * (Input's `error`); content that failed to load is ErrorState.
 */
export default function Alert({ tone = 'info', title, children, className }: AlertProps) {
  const t = tones[tone]
  return (
    <div
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cn('flex items-start gap-2.5 rounded-control px-3 py-2.5 text-small', t.box, className)}
    >
      <span className="mt-0.5 flex shrink-0">{t.icon}</span>
      <div className="min-w-0 break-words">
        {title && <p className="font-semibold">{title}</p>}
        {children && <div className={cn(title && 'mt-0.5')}>{children}</div>}
      </div>
    </div>
  )
}
