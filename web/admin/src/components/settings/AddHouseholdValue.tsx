import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchAllHouseholds } from '@/api/admin'
import { Alert, Button, ErrorState, Select, Skeleton, toast } from '@/components/ui'
import { useUpdateSetting } from '@/hooks/useSettings'
import { errorMessage } from '@/lib/errors'
import type { SettingResponse } from '@/types/settings'
import SettingEditor from './SettingEditor'

interface Props {
  setting: SettingResponse
  serviceName: string
  onClose: () => void
}

/** Mounted on demand: the household list is shared between open settings editors. */
export default function AddHouseholdValue({ setting, serviceName, onClose }: Props) {
  const { data, isPending, error, refetch } = useQuery({ queryKey: ['admin', 'households'], queryFn: fetchAllHouseholds })
  const [householdId, setHouseholdId] = useState('')
  const update = useUpdateSetting()
  const existing = new Set(setting.household_values?.map((h) => h.household_id))
  const choices = data?.filter((h) => !existing.has(h.id)).sort((a, b) => a.name.localeCompare(b.name)) ?? []
  const selected = choices.find((h) => h.id === householdId)

  const save = (value: unknown) => {
    if (!selected) return
    update.mutate({ serviceName, key: setting.key, value, householdId: selected.id }, {
      onSuccess: () => {
        toast.success(`Updated for ${selected.name}`)
        onClose()
      },
    })
  }

  return (
    <fieldset disabled={update.isPending} className="min-w-0 space-y-3 py-3">
      <legend className="sr-only">Set {setting.key} for a household</legend>
      {isPending ? (
        <div role="status" aria-label="Loading households"><Skeleton className="h-10 w-full" /></div>
      ) : error ? (
        <ErrorState title="Couldn't load households" message={errorMessage(error)} onRetry={() => refetch()} />
      ) : choices.length === 0 ? (
        <p className="text-small text-text-muted">No households available.</p>
      ) : (
        <Select
          label="Household"
          value={householdId}
          placeholder="Choose a household"
          options={choices.map((h) => ({ value: h.id, label: h.name }))}
          onValueChange={(id) => { setHouseholdId(id); update.reset() }}
        />
      )}
      {selected ? (
        <SettingEditor
          key={selected.id}
          setting={{ ...setting, value: setting.is_secret ? '' : setting.value, from_db: false }}
          onSave={save}
          onCancel={onClose}
          isSaving={update.isPending}
        />
      ) : <Button variant="ghost" onClick={onClose}>Cancel</Button>}
      {update.error && <Alert tone="danger">{errorMessage(update.error, "Couldn't save the household setting")}</Alert>}
    </fieldset>
  )
}
