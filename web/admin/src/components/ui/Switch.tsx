import { useId, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export interface SwitchProps {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  /** The visible label, after the switch. Without one, pass aria-label. */
  label?: ReactNode
  /** A line under the label. */
  description?: ReactNode
  disabled?: boolean
  id?: string
  'aria-label'?: string
  className?: string
}

/**
 * Switch is an on/off control (role=switch). The track's colour changes in 120ms and the thumb
 * slides in 160ms; reduced motion keeps the colour change and moves the thumb at once.
 */
export default function Switch({
  checked,
  onCheckedChange,
  label,
  description,
  disabled,
  id,
  className,
  'aria-label': ariaLabel,
}: SwitchProps) {
  const auto = useId()
  const switchId = id ?? auto
  const control = (
    <button
      type="button"
      role="switch"
      id={switchId}
      aria-checked={checked}
      aria-label={ariaLabel}
      aria-describedby={description ? `${switchId}-description` : undefined}
      disabled={disabled}
      onClick={() => onCheckedChange(!checked)}
      className={cn(
        'relative inline-flex h-6 w-10 shrink-0 items-center rounded-pill border',
        'transition-[background-color,border-color] duration-(--duration-hover) ease-out',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'border-primary bg-primary' : 'border-border-strong bg-surface-alt',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'inline-block size-4.5 rounded-pill shadow-raised',
          'transition-[translate,background-color] duration-(--duration-menu) ease-out motion-reduce:transition-[background-color]',
          checked ? 'translate-x-[1.125rem] bg-on-primary' : 'translate-x-0.5 bg-border-strong',
        )}
      />
    </button>
  )
  if (!label) return <span className={className}>{control}</span>
  return (
    <div className={cn('flex items-start gap-3', className)}>
      <span className="pt-0.5">{control}</span>
      <div className="min-w-0">
        <label htmlFor={switchId} className="text-body text-text">
          {label}
        </label>
        {description && (
          <p id={`${switchId}-description`} className="text-small text-text-muted">
            {description}
          </p>
        )}
      </div>
    </div>
  )
}
