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

/**
 * buttonClass is Button's look, for an element that has to stay something else: a router Link
 * or an external link that reads as a button. Apart from Button.tsx for fast refresh.
 */
export function buttonClass({ variant = 'secondary', size = 'md' }: { variant?: ButtonVariant; size?: ButtonSize } = {}) {
  return cn(
    'press inline-flex shrink-0 items-center justify-center rounded-control font-medium whitespace-nowrap select-none',
    'disabled:cursor-not-allowed disabled:opacity-50',
    variants[variant],
    sizes[size],
  )
}
