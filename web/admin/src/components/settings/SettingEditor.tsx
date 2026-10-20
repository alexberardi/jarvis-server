import { useState, type FormEvent } from 'react'
import { cn } from '@/lib/utils'
import { Button, Input, Select, Switch } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import type { SettingResponse } from '@/types/settings'
import { secretIsSet } from '@/lib/settings'

interface SettingEditorProps {
  setting: SettingResponse
  onSave: (value: unknown) => void
  onCancel: () => void
  isSaving: boolean
}

const OTHER = '__other__'

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

  const onText = (v: string) => {
    setRawValue(v)
    setValidationError(null)
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-wrap items-start gap-2">
      {hasOptions ? (
        <>
          <Select
            aria-label={setting.key}
            wrapperClassName="w-full sm:w-64"
            value={showCustomInput ? OTHER : rawValue}
            onValueChange={(v) => {
              if (v === OTHER) {
                setShowCustomInput(true)
                setRawValue('')
              } else {
                setShowCustomInput(false)
                setRawValue(v)
              }
            }}
            options={[
              ...setting.options!.map((opt) => ({ value: String(opt), label: String(opt) })),
              { value: OTHER, label: 'Other…' },
            ]}
          />
          {showCustomInput && (
            <Input
              aria-label={`${setting.key}, custom value`}
              wrapperClassName="w-full sm:w-64"
              value={rawValue}
              onChange={(e) => setRawValue(e.target.value)}
              placeholder="Custom value"
              autoFocus
            />
          )}
        </>
      ) : setting.value_type === 'bool' ? (
        <Switch
          className="flex h-10 items-center"
          aria-label={setting.key}
          checked={boolValue}
          onCheckedChange={setBoolValue}
        />
      ) : setting.value_type === 'json' ? (
        <div className="flex w-full flex-col gap-1.5">
          <textarea
            aria-label={setting.key}
            aria-invalid={validationError ? true : undefined}
            value={rawValue}
            onChange={(e) => onText(e.target.value)}
            rows={4}
            className={cn(fieldControl, 'h-auto py-2 font-mono text-small')}
          />
          {validationError && <p className="text-small text-danger">{validationError}</p>}
        </div>
      ) : (
        <Input
          aria-label={setting.key}
          wrapperClassName="w-full sm:w-64"
          type={setting.value_type === 'int' || setting.value_type === 'float' ? 'number' : setting.is_secret ? 'password' : 'text'}
          step={setting.value_type === 'float' ? 'any' : undefined}
          autoComplete={setting.is_secret ? 'off' : undefined}
          placeholder={setting.is_secret ? (secretIsSet(setting) ? 'Enter a new value to replace it' : 'Paste value') : undefined}
          value={rawValue}
          onChange={(e) => onText(e.target.value)}
          error={validationError ?? undefined}
        />
      )}

      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={isSaving}>
          Save
        </Button>
        {setting.is_secret && secretIsSet(setting) && (
          <Button
            variant="danger"
            disabled={isSaving}
            onClick={() => onSave(null)}
            title="Remove the stored value (falls back to the default or environment)"
          >
            Clear
          </Button>
        )}
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
