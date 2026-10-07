import { useState, useMemo } from 'react'
import { ChevronDown, ChevronRight, CheckCircle2, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import CategoryGroup from './CategoryGroup'
import type { ServiceSettingsResult } from '@/types/settings'

interface ServiceCardProps {
  result: ServiceSettingsResult
  defaultExpanded?: boolean
}

export default function ServiceCard({ result, defaultExpanded = false }: ServiceCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)
  const grouped = useMemo(() => {
    const map = new Map<string, typeof result.settings>()
    for (const s of result.settings) {
      const existing = map.get(s.category) ?? []
      map.set(s.category, [...existing, s])
    }
    return map
  }, [result])

  return (
    <div className="overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
      <button
        onClick={() => setExpanded((prev) => !prev)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-[var(--color-surface-alt)]"
      >
        {expanded ? (
          <ChevronDown size={18} className="text-[var(--color-text-muted)]" />
        ) : (
          <ChevronRight size={18} className="text-[var(--color-text-muted)]" />
        )}

        <span className="font-medium text-[var(--color-text)]">
          {result.display_name || result.service_name}
        </span>
        {result.display_name && (
          <code className="text-xs text-[var(--color-text-muted)]">{result.service_name}</code>
        )}

        {result.success ? (
          <CheckCircle2 size={16} className="text-[var(--color-secondary)]" />
        ) : (
          <XCircle size={16} className="text-red-500" />
        )}

        <span className="text-xs text-[var(--color-text-muted)]">
          {result.success
            ? `${result.settings.length} settings`
            : result.error ?? 'Error'}
        </span>

        {result.latency_ms != null && (
          <span
            className={cn(
              'ml-auto text-xs',
              result.latency_ms > 500 ? 'text-amber-500' : 'text-[var(--color-text-muted)]',
            )}
          >
            {Math.round(result.latency_ms)}ms
          </span>
        )}
      </button>

      {expanded && result.success && (
        <div className="space-y-2 border-t border-[var(--color-border)] px-2 py-3">
          {[...grouped.entries()].map(([category, settings]) => (
            <CategoryGroup
              key={category}
              category={category}
              settings={settings}
              serviceName={result.service_name}
            />
          ))}
        </div>
      )}

      {expanded && !result.success && (
        <div className="border-t border-[var(--color-border)] px-4 py-3">
          <p className="text-sm text-red-500">{result.error ?? 'Failed to fetch settings'}</p>
        </div>
      )}
    </div>
  )
}
