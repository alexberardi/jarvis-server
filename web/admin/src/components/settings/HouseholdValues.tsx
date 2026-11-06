import { useState } from 'react'
import { Pencil } from 'lucide-react'
import { toast } from 'sonner'
import { useResetHouseholdSetting, useUpdateSetting } from '@/hooks/useSettings'
import { errorMessage } from '@/lib/errors'
import { formatSettingValue } from '@/lib/settings'
import type { HouseholdValue, SettingResponse } from '@/types/settings'
import { Button } from '@/components/ui'
import SettingEditor from './SettingEditor'
import AddHouseholdValue from './AddHouseholdValue'

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
  const [adding, setAdding] = useState(false)
  const values = setting.household_values ?? []
  const usingDefault = setting.households_using_default

  return (
    <div className="mt-2 rounded-control bg-surface-alt px-3 py-1" data-testid={`household-values-${setting.key}`}>
      <p className="pt-1 text-caption text-text-muted">{values.length ? 'Households with their own value' : 'No household overrides.'}</p>
      {values.map((hv) => (
        <HouseholdValueRow key={hv.household_id} setting={setting} serviceName={serviceName} hv={hv} />
      ))}
      {usingDefault !== undefined && usingDefault > 0 && (
        <p className="pb-1 text-caption text-text-muted">
          {plural(usingDefault, 'household uses', 'households use')} the default
        </p>
      )}
      {adding ? (
        <AddHouseholdValue setting={setting} serviceName={serviceName} onClose={() => setAdding(false)} />
      ) : usingDefault === undefined || usingDefault > 0 ? (
        <Button size="sm" className="my-2" onClick={() => setAdding(true)}>Set for a household</Button>
      ) : null}
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
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 py-1 text-small" data-testid={`household-value-${hv.household_id}`}>
      <span className="min-w-0 shrink truncate font-medium text-text" title={hv.household_id}>
        {hv.household_name}
      </span>
      {editing ? (
        <div className="w-full">
          <SettingEditor
            setting={own}
            onSave={handleSave}
            onCancel={() => setEditing(false)}
            isSaving={update.isPending}
          />
        </div>
      ) : (
        <>
          <span className="min-w-0 font-mono break-all text-text-muted">{formatSettingValue(own)}</span>
          <span className="flex-1" />
          <Button
            variant="ghost"
            size="icon"
            className="size-8"
            onClick={() => setEditing(true)}
            disabled={busy}
            aria-label={`Edit ${hv.household_name}`}
            title={`Edit ${hv.household_name}'s value`}
          >
            <Pencil size={14} aria-hidden />
          </Button>
          <Button
            size="sm"
            onClick={handleReset}
            loading={reset.isPending}
            disabled={busy}
            title={`Remove ${hv.household_name}'s own value; it follows the default again`}
          >
            Use default
          </Button>
        </>
      )}
    </div>
  )
}
