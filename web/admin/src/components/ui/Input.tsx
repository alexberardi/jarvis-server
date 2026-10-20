import { useId, type InputHTMLAttributes, type Ref } from 'react'
import { cn } from '@/lib/utils'
import { FieldFrame } from './Field'
import { fieldControl, noteProps, type FieldProps } from './fieldStyles'

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size'>, FieldProps {
  /** Classes for the wrapper (label, field and note); className styles the field. */
  wrapperClassName?: string
  ref?: Ref<HTMLInputElement>
}

/** Input is a text field with its label, hint and error wired up for screen readers. */
export default function Input({ label, hint, error, id, className, wrapperClassName, ref, ...rest }: InputProps) {
  const auto = useId()
  const fieldId = id ?? auto
  return (
    <FieldFrame id={fieldId} label={label} hint={hint} error={error} className={wrapperClassName}>
      <input ref={ref} id={fieldId} className={cn(fieldControl, className)} {...noteProps(fieldId, { hint, error })} {...rest} />
    </FieldFrame>
  )
}
