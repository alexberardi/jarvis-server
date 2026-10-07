import { useState, useMemo, useCallback } from 'react'
import { ChevronDown, ChevronRight, CheckCircle2, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { toast } from 'sonner'
import CategoryGroup from './CategoryGroup'
import { useContainers, useRestartContainer } from '@/hooks/useContainers'
import type { ServiceSettingsResult } from '@/types/settings'

interface ServiceCardProps {
  result: ServiceSettingsResult
  defaultExpanded?: boolean
}

export default function ServiceCard({ result, defaultExpanded = false }: ServiceCardProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)
  const { data: containersData } = useContainers()
  const restartMutation = useRestartContainer()

  const handleRestart = useCallback(() => {
    const container = containersData?.containers.find(
      (c) => c.name.includes(result.service_name) || c.name.includes(result.service_name.replace('jarvis-', '')),
    )
    if (container) {
      restartMutation.mutate(container.id, {
        onSuccess: () => toast.success(`${result.service_name} is restarting...`),
        onError: (err) => toast.error(`Restart failed: ${err.message}`),
      })
    } else {
      toast.error(`Could not find container for ${result.service_name}`)
    }
  }, [containersData, result.service_name, restartMutation])

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

        <span className="font-medium text-[var(--color-text)]">{result.service_name}</span>

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
              onRestartService={handleRestart}
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
