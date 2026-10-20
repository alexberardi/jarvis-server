import { cn } from '@/lib/utils'

/** Skeleton is one placeholder block. It is hidden from screen readers; wrap it in SkeletonList or give its container a status. */
export default function Skeleton({ className }: { className?: string }) {
  return <span aria-hidden className={cn('block h-4 animate-pulse rounded-control bg-surface-alt', className)} />
}

interface SkeletonListProps {
  /** How many placeholder rows; match the expected count so the layout doesn't jump. */
  rows?: number
  /** What is loading, read out once. */
  label?: string
  className?: string
}

/** SkeletonList stands in for a List while it loads: 44px rows of a title and a meta line. */
export function SkeletonList({ rows = 3, label = 'Loading', className }: SkeletonListProps) {
  return (
    <div role="status" aria-label={label} className={cn('divide-y divide-border', className)}>
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex min-h-11 items-center gap-3 px-4 py-2">
          <Skeleton className="size-6 shrink-0 rounded-pill" />
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3.5 w-2/5" />
            <Skeleton className="h-3 w-3/5" />
          </div>
        </div>
      ))}
    </div>
  )
}
