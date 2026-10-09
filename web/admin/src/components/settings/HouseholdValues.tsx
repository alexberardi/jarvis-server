import { useState } from 'react'
import { Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { useResetHouseholdSetting, useUpdateSetting } from '@/hooks/useSettings'
import { errorMessage } from '@/lib/errors'
import { formatSettingValue } from '@/lib/settings'
import type { HouseholdValue, SettingResponse } from '@/types/settings'
import SettingEditor from './SettingEditor'

interface HouseholdValuesProps {
  setting: SettingResponse
  serviceName: string
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

/**
 * HouseholdValues lists the households that set their own value of a household-scoped setting
 * (in the mobile app, or here), each with an editor and "Use default", under the row's default.
 */
export default function HouseholdValues({ setting, serviceName }: HouseholdValuesProps) {
  const values = setting.household_values ?? []
  const usingDefault = setting.households_using_default

  if (values.length === 0) {
    return (
      <p className="mt-1 text-[11px] italic text-[var(--color-text-muted)]">
        Households can change this in the app
      </p>
    )
  }

  return (
    <div
      className="mt-2 space-y-0.5 border-l-2 border-[var(--color-border)] pl-3"
      data-testid={`household-values-${setting.key}`}
    >
      {values.map((hv) => (
        <HouseholdValueRow key={hv.household_id} setting={setting} serviceName={serviceName} hv={hv} />
      ))}
      {usingDefault !== undefined && usingDefault > 0 && (
        <p className="text-[11px] text-[var(--color-text-muted)]">
          {plural(usingDefault, 'household uses', 'households use')} the default
        </p>
      )}
    </div>
  )
}

interface HouseholdValueRowProps {
  setting: SettingResponse
  serviceName: string
  hv: HouseholdValue
}

function HouseholdValueRow({ setting, serviceName, hv }: HouseholdValueRowProps) {
  const [editing, setEditing] = useState(false)
  const update = useUpdateSetting()
  const reset = useResetHouseholdSetting()
  const own: SettingResponse = { ...setting, value: hv.value }
  const busy = update.isPending || reset.isPending

  const handleSave = (value: unknown) => {
    update.mutate(
      { serviceName, key: setting.key, value, householdId: hv.household_id },
      {
        onSuccess: () => {
          setEditing(false)
          toast.success(`Updated for ${hv.household_name}`)
        },
        onError: (err) => toast.error(`Failed to update: ${errorMessage(err)}`),
      },
    )
  }

  const handleReset = () => {
    reset.mutate(
      { serviceName, key: setting.key, householdId: hv.household_id },
      {
        onSuccess: () => toast.success(`${hv.household_name} now uses the default`),
        onError: (err) => toast.error(`Failed to reset: ${errorMessage(err)}`),
      },
    )
  }

  return (
    <div className="flex items-center gap-2 text-xs" data-testid={`household-value-${hv.household_id}`}>
      <span className="min-w-0 shrink truncate text-[var(--color-text)]" title={hv.household_id}>
        {hv.household_name}
      </span>
      {editing ? (
        <div className="flex-1">
          <SettingEditor
            setting={own}
            onSave={handleSave}
            onCancel={() => setEditing(false)}
            isSaving={update.isPending}
          />
        </div>
      ) : (
        <>
          <span className="font-mono text-[var(--color-text-muted)]">{formatSettingValue(own)}</span>
          <span className="flex-1" />
          <button
            type="button"
            onClick={() => setEditing(true)}
            disabled={busy}
            aria-label={`Edit ${hv.household_name}`}
            title={`Edit ${hv.household_name}'s value`}
            className="shrink-0 rounded p-1 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-primary)] disabled:opacity-50"
          >
            <Pencil size={12} />
          </button>
          <button
            type="button"
            onClick={handleReset}
            disabled={busy}
            title={`Remove ${hv.household_name}'s own value; it follows the default again`}
            className="shrink-0 rounded border border-[var(--color-border)] px-2 py-0.5 text-[11px] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)] disabled:opacity-50"
          >
            {reset.isPending ? 'Resetting…' : 'Use default'}
          </button>
        </>
      )}
    </div>
  )
}
