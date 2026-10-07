import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import SettingRow from './SettingRow'
import type { SettingResponse } from '@/types/settings'

interface CategoryGroupProps {
  category: string
  settings: SettingResponse[]
  serviceName: string
}

export default function CategoryGroup({ category, settings, serviceName }: CategoryGroupProps) {
  const [expanded, setExpanded] = useState(true)

  return (
    <div>
      <button
        onClick={() => setExpanded((prev) => !prev)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
      >
        {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        {category}
        <span className="font-normal">({settings.length})</span>
      </button>

      {expanded && (
        <div className="space-y-0.5">
          {settings.map((setting) => (
            <SettingRow key={setting.key} setting={setting} serviceName={serviceName} />
          ))}
        </div>
      )}
    </div>
  )
}
