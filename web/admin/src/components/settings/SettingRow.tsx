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

  return (
    <div className="flex flex-col gap-2 px-4 py-3 md:flex-row md:items-start md:gap-6">
      <div className="min-w-0 md:flex-1">
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
        {setting.description && <p className="mt-1 max-w-prose text-small text-text-muted">{setting.description}</p>}
        {setting.household_scoped && <p className="mt-1 text-caption text-text-muted">Default for all households</p>}
      </div>

      <div className="min-w-0 md:w-80 md:shrink-0">
        {editing ? (
          <SettingEditor
            setting={setting}
            onSave={handleSave}
            onCancel={() => setEditing(false)}
            isSaving={mutation.isPending}
          />
        ) : (
          <div className="flex min-h-10 items-center gap-2">
            <p className="min-w-0 flex-1 font-mono text-small break-all text-text">{formatSettingValue(setting)}</p>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => setEditing(true)}
              title={setting.household_scoped ? 'Edit the default' : 'Edit'}
              aria-label={setting.household_scoped ? 'Edit the default' : 'Edit'}
            >
              <Pencil size={16} aria-hidden />
            </Button>
          </div>
        )}
        {setting.household_scoped && <HouseholdValues setting={setting} serviceName={serviceName} />}
      </div>
    </div>
  )
}
