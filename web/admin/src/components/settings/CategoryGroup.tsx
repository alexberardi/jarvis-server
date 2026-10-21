import { useId, useState } from 'react'
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
  const panel = useId()

  return (
    <div>
      <button
        type="button"
        onClick={() => setExpanded((prev) => !prev)}
        aria-expanded={expanded}
        aria-controls={panel}
        className="flex min-h-10 w-full items-center gap-2 bg-surface-alt px-4 text-left text-small font-medium text-text-muted transition-colors duration-(--duration-hover) hover:text-text"
      >
        {expanded ? <ChevronDown size={16} aria-hidden /> : <ChevronRight size={16} aria-hidden />}
        <span className="capitalize">{category}</span>
        <span className="font-normal tabular-nums">{settings.length}</span>
      </button>

      {expanded && (
        <div id={panel} className="divide-y divide-border">
          {settings.map((setting) => (
            <SettingRow key={setting.key} setting={setting} serviceName={serviceName} />
          ))}
        </div>
      )}
    </div>
  )
}
