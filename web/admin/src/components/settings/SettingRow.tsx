import { useState } from 'react'
import { Pencil, Database, AlertTriangle, Lock } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useUpdateSetting } from '@/hooks/useSettings'
import SettingEditor from './SettingEditor'
import type { SettingResponse } from '@/types/settings'
import { toast } from 'sonner'
import { errorMessage } from '@/lib/errors'
import { restartAction, secretIsSet } from '@/lib/settings'

interface SettingRowProps {
  setting: SettingResponse
  serviceName: string
}

const RELOAD_MESSAGE = 'Applies after jarvisd restarts'

function formatValue(setting: SettingResponse): string {
  // Secrets are write-only (I5): never echo even the mask as if it were a value.
  if (setting.is_secret) return secretIsSet(setting) ? 'set (hidden)' : 'not set'
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
              action ? { action: { label: 'Restart jarvisd', onClick: action } } : undefined,
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
          {setting.is_secret && (
            <span title="Secret: write-only">
              <Lock size={12} className="text-[var(--color-text-muted)]" />
            </span>
          )}
          {setting.requires_reload && (
            <span title={RELOAD_MESSAGE}>
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
