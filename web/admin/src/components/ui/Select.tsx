import { useId, type Ref, type SelectHTMLAttributes } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import { FieldFrame } from './Field'
import { fieldControl, noteProps, type FieldProps } from './fieldStyles'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'onChange'>, FieldProps {
  options: SelectOption[]
  /** Called with the picked option's value. */
  onValueChange?: (value: string) => void
  onChange?: SelectHTMLAttributes<HTMLSelectElement>['onChange']
  /** An empty first option shown while nothing is picked. */
  placeholder?: string
  wrapperClassName?: string
  ref?: Ref<HTMLSelectElement>
}

/**
 * Select is the native select, styled. The OS draws the open list (the phone's own picker, a
 * keyboard-friendly menu on desktop), so there is no menu animation to tune here.
 */
export default function Select({
  label,
  hint,
  error,
  id,
  options,
  onValueChange,
  onChange,
  placeholder,
  className,
  wrapperClassName,
  ref,
  ...rest
}: SelectProps) {
  const auto = useId()
  const fieldId = id ?? auto
  return (
    <FieldFrame id={fieldId} label={label} hint={hint} error={error} className={wrapperClassName}>
      <div className="relative">
        <select
          ref={ref}
          id={fieldId}
          className={cn(fieldControl, 'appearance-none pr-9', className)}
          onChange={(e) => {
            onChange?.(e)
            onValueChange?.(e.target.value)
          }}
          {...noteProps(fieldId, { hint, error })}
          {...rest}
        >
          {placeholder !== undefined && (
            <option value="" disabled>
              {placeholder}
            </option>
          )}
          {options.map((o) => (
            <option key={o.value} value={o.value} disabled={o.disabled}>
              {o.label}
            </option>
          ))}
        </select>
        <ChevronDown
          size={16}
          aria-hidden
          className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-text-muted"
        />
      </div>
    </FieldFrame>
  )
}
