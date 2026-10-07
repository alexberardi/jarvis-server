import { useState } from 'react'
import { Pencil, Database, AlertTriangle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useUpdateSetting } from '@/hooks/useSettings'
import SettingEditor from './SettingEditor'
import type { SettingResponse } from '@/types/settings'
import { toast } from 'sonner'

interface SettingRowProps {
  setting: SettingResponse
  serviceName: string
  onRestartService?: () => void
}

function formatValue(setting: SettingResponse): string {
  if (setting.is_secret) return '********'
  if (setting.value === null || setting.value === undefined) return '(not set)'
  if (setting.value_type === 'json') return JSON.stringify(setting.value)
  return String(setting.value)
}

const typeBadgeColors: Record<string, string> = {
  string: 'bg-blue-500/10 text-blue-500',
  int: 'bg-emerald-500/10 text-emerald-500',
  float: 'bg-emerald-500/10 text-emerald-500',
  bool: 'bg-amber-500/10 text-amber-500',
  json: 'bg-purple-500/10 text-purple-500',
}

export default function SettingRow({ setting, serviceName, onRestartService }: SettingRowProps) {
  const [editing, setEditing] = useState(false)
  const mutation = useUpdateSetting()

  const handleSave = (value: unknown) => {
    mutation.mutate(
      { serviceName, key: setting.key, value },
      {
        onSuccess: (res) => {
          setEditing(false)
          if (res.requires_reload) {
            toast.warning(`Setting updated. ${serviceName} requires a restart to apply.`, onRestartService ? {
              action: {
                label: 'Restart',
                onClick: onRestartService,
              },
            } : undefined)
          } else {
            toast.success('Setting updated')
          }
        },
        onError: (err) => {
          toast.error(`Failed to update: ${err.message}`)
        },
      },
    )
  }

  return (
    <div className="flex items-start justify-between gap-4 rounded-lg px-3 py-2 hover:bg-[var(--color-surface-alt)]">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <code className="text-sm font-medium text-[var(--color-text)]">{setting.key}</code>
          <span
            className={cn(
              'rounded px-1.5 py-0.5 text-[10px] font-medium',
              typeBadgeColors[setting.value_type] ?? 'bg-gray-500/10 text-gray-500',
            )}
          >
            {setting.value_type}
          </span>
          {setting.from_db && (
            <span title="Stored in database">
              <Database size={12} className="text-[var(--color-tertiary)]" />
            </span>
          )}
          {setting.requires_reload && (
            <span title="Requires service restart">
              <AlertTriangle size={12} className="text-amber-500" />
            </span>
          )}
        </div>

        {setting.description && (
          <p className="mt-0.5 text-xs text-[var(--color-text-muted)]">{setting.description}</p>
        )}

        {editing ? (
          <div className="mt-2">
            <SettingEditor
              setting={setting}
              onSave={handleSave}
              onCancel={() => setEditing(false)}
              isSaving={mutation.isPending}
            />
          </div>
        ) : (
          <p className="mt-1 font-mono text-xs text-[var(--color-text-muted)]">
            {formatValue(setting)}
          </p>
        )}
      </div>

      {!editing && (
        <button
          onClick={() => setEditing(true)}
          className="shrink-0 rounded-lg p-1.5 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-primary)]"
          title="Edit"
        >
          <Pencil size={14} />
        </button>
      )}
    </div>
  )
}
