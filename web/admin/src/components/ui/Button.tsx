import type { ButtonHTMLAttributes, ReactNode, Ref } from 'react'
import Spinner from './Spinner'
import { buttonStyles, type ButtonSize, type ButtonVariant } from './buttonStyles'

export type { ButtonSize, ButtonVariant }

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  /** Shows a spinner and blocks clicks while an action runs. The label stays, so the width does too. */
  loading?: boolean
  /** An icon before the label. */
  icon?: ReactNode
  ref?: Ref<HTMLButtonElement>
}

/** Button is the one button: four variants, three sizes, a loading state and B's press feedback. */
export default function Button({
  variant = 'secondary',
  size = 'md',
  loading = false,
  icon,
  disabled,
  className,
  children,
  type = 'button',
  ref,
  ...rest
}: ButtonProps) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={buttonStyles({ variant, size, className })}
      {...rest}
    >
      {loading ? <Spinner /> : icon}
      {children}
    </button>
  )
}
