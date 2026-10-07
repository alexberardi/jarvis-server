import { RotateCw, ExternalLink } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ContainerInfo } from '@/types/containers'

interface ServiceHealthCardProps {
  container: ContainerInfo
  onRestart: (id: string) => void
  isRestarting: boolean
}

const stateColors: Record<string, string> = {
  running: 'bg-emerald-500',
  exited: 'bg-red-500',
  restarting: 'bg-amber-500',
  paused: 'bg-amber-500',
  created: 'bg-gray-400',
  dead: 'bg-red-700',
}

function getUptime(status: string): string | null {
  const match = status.match(/^Up (.+)$/)
  return match?.[1] ?? null
}

export default function ServiceHealthCard({
  container,
  onRestart,
  isRestarting,
}: ServiceHealthCardProps) {
  const uptime = getUptime(container.status)
  const port = container.ports.find((p) => p.public)?.public

  return (
    <div className="overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="flex items-center gap-3 px-4 py-3">
        <div
          className={cn(
            'h-2.5 w-2.5 rounded-full',
            stateColors[container.state] ?? 'bg-gray-400',
          )}
        />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-[var(--color-text)]">
            {container.displayName ?? container.name}
          </p>
          {container.description && (
            <p className="truncate text-xs text-[var(--color-text-muted)]">
              {container.description}
            </p>
          )}
        </div>
        <span
          className={cn(
            'rounded-full px-2 py-0.5 text-xs font-medium',
            container.state === 'running'
              ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
              : 'bg-red-500/10 text-red-600 dark:text-red-400',
          )}
        >
          {container.state}
        </span>
      </div>

      <div className="border-t border-[var(--color-border)] px-4 py-2">
        <div className="flex items-center justify-between text-xs text-[var(--color-text-muted)]">
          <div className="flex items-center gap-3">
            {uptime && <span>Up {uptime}</span>}
            {port && <span>:{port}</span>}
          </div>

          <div className="flex items-center gap-1">
            {port && (
              <a
                href={`http://localhost:${port}/health`}
                target="_blank"
                rel="noopener noreferrer"
                className="rounded p-1 hover:bg-[var(--color-surface-alt)]"
                title="Open health endpoint"
              >
                <ExternalLink size={13} />
              </a>
            )}
            <button
              onClick={() => onRestart(container.id)}
              disabled={isRestarting}
              className={cn(
                'rounded p-1 hover:bg-[var(--color-surface-alt)]',
                isRestarting && 'animate-spin',
              )}
              title="Restart"
            >
              <RotateCw size={13} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
