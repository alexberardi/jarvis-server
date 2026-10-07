import { useState, type FormEvent } from 'react'
import { cn } from '@/lib/utils'
import type { SettingResponse } from '@/types/settings'
import { secretIsSet } from '@/lib/settings'

interface SettingEditorProps {
  setting: SettingResponse
  onSave: (value: unknown) => void
  onCancel: () => void
  isSaving: boolean
}

export default function SettingEditor({ setting, onSave, onCancel, isSaving }: SettingEditorProps) {
  const [rawValue, setRawValue] = useState(() => {
    // Write-only: a secret's editor starts empty, never with the mask (I5).
    if (setting.is_secret) return ''
    if (setting.value_type === 'json') return JSON.stringify(setting.value, null, 2)
    if (setting.value_type === 'bool') return ''
    return String(setting.value ?? '')
  })
  const [boolValue, setBoolValue] = useState(() => Boolean(setting.value))
  const [validationError, setValidationError] = useState<string | null>(null)
  const hasOptions = !setting.is_secret && Boolean(setting.options?.length)
  const isCustomValue = hasOptions && !setting.options!.includes(rawValue)
  const [showCustomInput, setShowCustomInput] = useState(isCustomValue)

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()

    if (setting.is_secret && setting.value_type === 'string' && !rawValue) {
      setValidationError(secretIsSet(setting) ? 'Enter a new value, or use Clear' : 'Enter a value')
      return
    }

    let parsed: unknown
    switch (setting.value_type) {
      case 'bool':
        parsed = boolValue
        break
      case 'int':
        parsed = parseInt(rawValue, 10)
        if (isNaN(parsed as number)) {
          setValidationError('Must be a whole number')
          return
        }
        setValidationError(null)
        break
      case 'float':
        parsed = parseFloat(rawValue)
        if (isNaN(parsed as number)) {
          setValidationError('Must be a number')
          return
        }
        setValidationError(null)
        break
      case 'json':
        try {
          parsed = JSON.parse(rawValue)
          setValidationError(null)
        } catch {
          setValidationError('Invalid JSON')
          return
        }
        break
      default:
        parsed = rawValue
    }

    onSave(parsed)
  }

  const inputClass = cn(
    'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-1.5',
    'text-sm text-[var(--color-text)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
  )

  return (
    <form onSubmit={handleSubmit} className="flex items-center gap-2">
      {hasOptions ? (
        <div className="flex items-center gap-2">
          <select
            value={showCustomInput ? '__other__' : rawValue}
            onChange={(e) => {
              if (e.target.value === '__other__') {
                setShowCustomInput(true)
                setRawValue('')
              } else {
                setShowCustomInput(false)
                setRawValue(e.target.value)
              }
            }}
            className={cn(inputClass, 'max-w-xs')}
          >
            {setting.options!.map((opt) => (
              <option key={String(opt)} value={String(opt)}>
                {String(opt)}
              </option>
            ))}
            <option value="__other__">Other...</option>
          </select>
          {showCustomInput && (
            <input
              type="text"
              value={rawValue}
              onChange={(e) => setRawValue(e.target.value)}
              placeholder="Custom value"
              className={cn(inputClass, 'max-w-xs')}
              autoFocus
            />
          )}
        </div>
      ) : setting.value_type === 'bool' ? (
        <button
          type="button"
          onClick={() => setBoolValue((prev) => !prev)}
          className={cn(
            'relative h-6 w-11 rounded-full transition-colors',
            boolValue ? 'bg-[var(--color-primary)]' : 'bg-[var(--color-border)]',
          )}
        >
          <span
            className={cn(
              'absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform',
              boolValue && 'translate-x-5',
            )}
          />
        </button>
      ) : setting.value_type === 'json' ? (
        <div className="flex-1">
          <textarea
            value={rawValue}
            onChange={(e) => {
              setRawValue(e.target.value)
              setValidationError(null)
            }}
            rows={4}
            className={cn(inputClass, 'font-mono text-xs')}
          />
          {validationError && <p className="mt-1 text-xs text-red-500">{validationError}</p>}
        </div>
      ) : (
        <div>
          <input
            type={setting.value_type === 'int' || setting.value_type === 'float' ? 'number' : setting.is_secret ? 'password' : 'text'}
            step={setting.value_type === 'float' ? 'any' : undefined}
            autoComplete={setting.is_secret ? 'off' : undefined}
            placeholder={
              setting.is_secret
                ? secretIsSet(setting)
                  ? 'Enter a new value to replace it'
                  : 'Paste value'
                : undefined
            }
            value={rawValue}
            onChange={(e) => {
              setRawValue(e.target.value)
              setValidationError(null)
            }}
            className={cn(inputClass, 'max-w-xs')}
          />
          {validationError && <p className="mt-1 text-xs text-red-500">{validationError}</p>}
        </div>
      )}

      <button
        type="submit"
        disabled={isSaving}
        className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs font-medium text-white hover:opacity-90 disabled:opacity-50"
      >
        {isSaving ? 'Saving...' : 'Save'}
      </button>
      {setting.is_secret && secretIsSet(setting) && (
        <button
          type="button"
          disabled={isSaving}
          onClick={() => onSave(null)}
          title="Remove the stored value (falls back to the default or environment)"
          className="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-red-500 hover:bg-red-500/10 disabled:opacity-50"
        >
          Clear
        </button>
      )}
      <button
        type="button"
        onClick={onCancel}
        className="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
      >
        Cancel
      </button>
    </form>
  )
}
