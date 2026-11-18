import { useId } from 'react'
import { cn } from '@/lib/utils'

interface ChoiceGroupProps<T extends string> {
  legend: string
  value: T
  onChange: (v: T) => void
  options: { value: T; label: string; disabled?: boolean }[]
  className?: string
}

/**
 * ChoiceGroup is a segmented one-of-n choice: native radios (arrow keys, one tab stop, a
 * fieldset legend) drawn as joined pills.
 */
export default function ChoiceGroup<T extends string>({ legend, value, onChange, options, className }: ChoiceGroupProps<T>) {
  const name = useId()
  return (
    <fieldset className={cn('min-w-0', className)}>
      <legend className="mb-1.5 text-small font-medium text-text">{legend}</legend>
      <div className="inline-flex rounded-control border border-border-strong bg-surface p-0.5">
        {options.map((o) => (
          <label
            key={o.value}
            className={cn(
              'relative cursor-pointer rounded-[calc(var(--radius-control)-2px)] px-3 py-1.5 text-small text-text-muted transition-colors duration-(--duration-hover)',
              'has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-1 has-[:focus-visible]:outline-primary',
              value === o.value && 'bg-primary-soft font-medium text-primary',
              o.disabled && 'cursor-not-allowed opacity-50',
            )}
          >
            <input
              type="radio"
              name={name}
              value={o.value}
              checked={value === o.value}
              disabled={o.disabled}
              onChange={() => onChange(o.value)}
              className="sr-only"
            />
            {o.label}
          </label>
        ))}
      </div>
    </fieldset>
  )
}
