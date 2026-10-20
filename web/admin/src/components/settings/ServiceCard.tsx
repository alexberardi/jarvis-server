import { useId, useState, useMemo } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { Badge, Card } from '@/components/ui'
import CategoryGroup from './CategoryGroup'
import type { ServiceSettingsResult } from '@/types/settings'

interface ServiceCardProps {
  result: ServiceSettingsResult
  defaultExpanded?: boolean
}

export default function ServiceCard({ result, defaultExpanded = false }: ServiceCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)
  const panel = useId()
  const grouped = useMemo(() => {
    const map = new Map<string, typeof result.settings>()
    for (const s of result.settings) {
      const existing = map.get(s.category) ?? []
      map.set(s.category, [...existing, s])
    }
    return map
  }, [result])
  const count = result.settings.length

  return (
    <Card flush className="overflow-hidden">
      <button
        type="button"
        onClick={() => setExpanded((prev) => !prev)}
        aria-expanded={expanded}
        aria-controls={panel}
        className="flex min-h-14 w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left transition-colors duration-(--duration-hover) hover:bg-surface-alt"
      >
        {expanded ? (
          <ChevronDown size={18} aria-hidden className="shrink-0 text-text-muted" />
        ) : (
          <ChevronRight size={18} aria-hidden className="shrink-0 text-text-muted" />
        )}
        <span className="min-w-0 text-subheading text-text">{result.display_name || result.service_name}</span>
        {result.display_name && <code className="font-mono text-caption text-text-muted">{result.service_name}</code>}
        <span className="ml-auto flex items-center gap-3">
          {result.latency_ms != null && result.latency_ms > 500 && (
            <span className="hidden text-caption text-warning tabular-nums sm:inline" title="Slow to answer">
              {Math.round(result.latency_ms)} ms
            </span>
          )}
          {result.success ? (
            <Badge>
              {count} {count === 1 ? 'setting' : 'settings'}
            </Badge>
          ) : (
            <Badge tone="danger" dot>
              Didn't load
            </Badge>
          )}
        </span>
      </button>

      {expanded && result.success && (
        <div id={panel} className="border-t border-border">
          {[...grouped.entries()].map(([category, settings]) => (
            <CategoryGroup key={category} category={category} settings={settings} serviceName={result.service_name} />
          ))}
        </div>
      )}

      {expanded && !result.success && (
        <div id={panel} className="border-t border-border px-4 py-3">
          <p className="text-small text-danger">{result.error ?? "This module didn't answer."}</p>
        </div>
      )}
    </Card>
  )
}
