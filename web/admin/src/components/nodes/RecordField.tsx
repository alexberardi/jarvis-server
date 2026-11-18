import type { FieldSpec } from '@/api/commandData'
import { Input, Select, Switch } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { canEdit, fromLocalInput, toLocalInput } from '@/lib/recordFields'
import { formatServerTime } from '@/lib/time'
import { cn } from '@/lib/utils'

const fieldLabel = (f: FieldSpec) => f.label || f.name.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())

/** shown is a stored value as text: Yes/No, a list joined, an object as JSON. */
function shown(f: FieldSpec, value: unknown, display?: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (f.type === 'bool') return value ? 'Yes' : 'No'
  if (f.type === 'user_ref' && typeof display === 'string' && display) return display
  if (f.type === 'datetime' && typeof value === 'string') return formatServerTime(value, { dateStyle: 'medium', timeStyle: 'short' })
  if (Array.isArray(value)) return value.map((v) => (typeof v === 'object' ? JSON.stringify(v) : String(v))).join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

/**
 * RecordField is one field of a stored record: an input for the types a form can edit, the
 * value as text otherwise (and whenever readOnly).
 */
export default function RecordField({
  f,
  value,
  display,
  readOnly,
  onChange,
}: {
  f: FieldSpec
  value: unknown
  display?: unknown
  readOnly: boolean
  onChange: (v: unknown) => void
}) {
  const label = fieldLabel(f) + (f.required && !readOnly ? ' (required)' : '')
  if (readOnly || !canEdit(f)) {
    return (
      <div>
        <p className="text-small font-medium text-text">{fieldLabel(f)}</p>
        <p className={cn('text-body break-words text-text', f.type === 'id' && 'font-mono text-small')}>{shown(f, value, display)}</p>
        {f.description && <p className="text-small text-text-muted">{f.description}</p>}
      </div>
    )
  }
  if (f.type === 'bool') return <Switch label={fieldLabel(f)} description={f.description} checked={value === true} onCheckedChange={onChange} />
  if (f.type === 'enum')
    return (
      <Select
        label={label}
        hint={f.description}
        value={typeof value === 'string' ? value : ''}
        placeholder="Choose…"
        options={f.enum_values!.map((v) => ({ value: v, label: v }))}
        onValueChange={onChange}
      />
    )
  if (f.type === 'text')
    return (
      <div className="space-y-1.5">
        <label htmlFor={`field-${f.name}`} className="block text-small font-medium text-text">
          {label}
        </label>
        <textarea
          id={`field-${f.name}`}
          rows={3}
          className={cn(fieldControl, 'h-auto py-2')}
          value={typeof value === 'string' ? value : ''}
          placeholder={f.placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
        {f.description && <p className="text-small text-text-muted">{f.description}</p>}
      </div>
    )
  if (f.type === 'int' || f.type === 'float')
    return (
      <Input
        label={label}
        hint={f.description}
        type="number"
        inputMode={f.type === 'int' ? 'numeric' : 'decimal'}
        step={f.type === 'int' ? 1 : 'any'}
        value={typeof value === 'number' ? String(value) : ''}
        placeholder={f.placeholder}
        // An emptied box is no value; otherwise the number (int fields keep whole numbers).
        onChange={(e) => onChange(e.target.value === '' ? null : f.type === 'int' ? Math.trunc(Number(e.target.value)) : Number(e.target.value))}
      />
    )
  const type = f.type === 'datetime' ? 'datetime-local' : f.type === 'date' || f.type === 'time' ? f.type : 'text'
  return (
    <Input
      label={label}
      hint={f.description}
      type={type}
      value={f.type === 'datetime' ? toLocalInput(value) : typeof value === 'string' ? value : value == null ? '' : String(value)}
      placeholder={f.placeholder}
      className={f.type === 'id' ? 'font-mono' : undefined}
      onChange={(e) => onChange(f.type === 'datetime' ? fromLocalInput(e.target.value) : e.target.value)}
    />
  )
}
