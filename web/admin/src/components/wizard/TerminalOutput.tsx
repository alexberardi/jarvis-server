import { useRef, useEffect } from 'react'
import { cn } from '@/lib/utils'
import type { StreamLine } from '@/hooks/useInstallStream'

interface TerminalOutputProps {
  lines: StreamLine[]
  running: boolean
  title?: string
  className?: string
}

export default function TerminalOutput({
  lines,
  running,
  title,
  className,
}: TerminalOutputProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const isNearBottomRef = useRef(true)

  useEffect(() => {
    const el = containerRef.current
    if (!el || !isNearBottomRef.current) return
    el.scrollTop = el.scrollHeight
  }, [lines])

  function handleScroll() {
    const el = containerRef.current
    if (!el) return
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    isNearBottomRef.current = distanceFromBottom < 50
  }

  return (
    <div className={cn('flex flex-col rounded-lg border border-[var(--color-border)] overflow-hidden', className)}>
      {title && (
        <div className="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
          <span className="text-xs font-medium text-[var(--color-text-muted)]">{title}</span>
          {running && (
            <span className="h-2 w-2 animate-pulse rounded-full bg-green-500" />
          )}
        </div>
      )}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className="h-64 overflow-y-auto bg-[#0d1117] p-3 font-mono text-xs leading-5"
      >
        {lines.length === 0 && !running && (
          <span className="text-gray-500">Waiting for output...</span>
        )}
        {lines.map((line, i) => (
          <div
            key={i}
            className={cn(
              'whitespace-pre-wrap break-all',
              line.stream === 'stderr' ? 'text-yellow-400' : 'text-gray-300',
            )}
          >
            {line.text}
          </div>
        ))}
        {running && (
          <span className="inline-block h-4 w-1.5 animate-pulse bg-gray-400" />
        )}
      </div>
    </div>
  )
}
