import { cn } from '@/lib/utils'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'icon'

const variants: Record<ButtonVariant, string> = {
  primary: 'bg-primary text-on-primary hover:bg-primary/90',
  secondary: 'border border-border-strong bg-surface text-text hover:bg-surface-alt',
  ghost: 'text-text-muted hover:bg-surface-alt hover:text-text',
  danger: 'bg-danger text-on-primary hover:bg-danger/90',
}

const sizes: Record<ButtonSize, string> = {
  sm: 'h-8 gap-1.5 px-3 text-small',
  md: 'h-10 gap-2 px-4 text-body',
  icon: 'size-10 justify-center',
}

/** buttonStyles is a Button's look, also for a link that should read as a button (apart from Button for fast refresh). */
export function buttonStyles({
  variant = 'secondary',
  size = 'md',
  className,
}: { variant?: ButtonVariant; size?: ButtonSize; className?: string } = {}) {
  return cn(
    'press inline-flex shrink-0 items-center justify-center rounded-control font-medium whitespace-nowrap select-none',
    'disabled:cursor-not-allowed disabled:opacity-50',
    variants[variant],
    sizes[size],
    className,
  )
}
