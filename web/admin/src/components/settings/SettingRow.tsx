import { useState } from 'react'
import { Database, Lock, Pencil, RotateCw } from 'lucide-react'
import { Badge, Button } from '@/components/ui'
import { useUpdateSetting } from '@/hooks/useSettings'
import SettingEditor from './SettingEditor'
import HouseholdValues from './HouseholdValues'
import type { SettingResponse } from '@/types/settings'
import { toast } from 'sonner'
import { errorMessage } from '@/lib/errors'
import { formatSettingValue, restartAction } from '@/lib/settings'

interface SettingRowProps {
  setting: SettingResponse
  serviceName: string
}

const RELOAD_MESSAGE = 'Applies after jarvisd restarts'

export default function SettingRow({ setting, serviceName }: SettingRowProps) {
  const [editing, setEditing] = useState(false)
  const mutation = useUpdateSetting()

  const handleSave = (value: unknown) => {
    mutation.mutate(
      { serviceName, key: setting.key, value },
      {
        onSuccess: (res) => {
          setEditing(false)
          if (res.requires_reload) {
            // AD8: offer a restart unless this jarvisd has no restart route.
            const action = restartAction()
            toast.warning(
              `Setting updated. ${res.message || RELOAD_MESSAGE}.`,
              action ? { action: { label: 'Restart server', onClick: action } } : undefined,
            )
          } else {
            toast.success('Setting updated')
          }
        },
        onError: (err) => {
          toast.error(`Failed to update: ${errorMessage(err)}`)
        },
      },
    )
  }

  const value = formatSettingValue(setting)
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-2 px-4 py-2.5">
      <div className="min-w-0 md:grid md:grid-cols-[minmax(0,1fr)_18rem] md:gap-x-6">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <code className="font-mono text-small font-medium break-all text-text">{setting.key}</code>
            <Badge>{setting.value_type}</Badge>
            {setting.is_secret && (
              <Badge title="Write-only: the value is never shown">
                <Lock size={12} aria-hidden />
                Secret
              </Badge>
            )}
            {setting.requires_reload && (
              <Badge tone="warning" title={RELOAD_MESSAGE}>
                <RotateCw size={12} aria-hidden />
                Needs restart
              </Badge>
            )}
            {setting.from_db && (
              <span role="img" aria-label="Changed from the default" title="Changed from the default (stored in the database)">
                <Database size={14} aria-hidden className="text-text-muted" />
              </span>
            )}
          </div>
          {setting.description && <p className="mt-0.5 max-w-prose text-small text-text-muted">{setting.description}</p>}
          {setting.household_scoped && <p className="mt-0.5 text-caption text-text-muted">Default for all households</p>}
        </div>

        <div className="mt-1 min-w-0 md:mt-0 md:pt-0.5">
          {editing ? (
            <SettingEditor
              setting={setting}
              onSave={handleSave}
              onCancel={() => setEditing(false)}
              isSaving={mutation.isPending}
            />
          ) : value ? (
            <p className="font-mono text-small break-all text-text">{value}</p>
          ) : (
            <p className="text-small text-text-muted italic">Empty</p>
          )}
          {setting.household_scoped && <HouseholdValues setting={setting} serviceName={serviceName} />}
        </div>
      </div>

      {!editing && (
        <Button
          variant="ghost"
          size="icon"
          className="-my-1.5 -mr-2"
          onClick={() => setEditing(true)}
          title={setting.household_scoped ? 'Edit the default' : 'Edit'}
          aria-label={setting.household_scoped ? 'Edit the default' : 'Edit'}
        >
          <Pencil size={16} aria-hidden />
        </Button>
      )}
    </div>
  )
}
