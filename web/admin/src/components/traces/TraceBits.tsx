import { Smartphone, Speaker } from 'lucide-react'
import { Badge } from '@/components/ui'
import { cn } from '@/lib/utils'

/** SourceIcon is where a request came from: the mobile app or a node (a speaker). */
export function SourceIcon({ source, className }: { source: string; className?: string }) {
  const Icon = source === 'mobile' ? Smartphone : Speaker
  return <Icon size={14} aria-hidden className={cn('shrink-0', className)} />
}

/** TraceStatus is a trace's outcome as a chip: "ok" in green, anything else in red. */
export function TraceStatus({ status }: { status: string }) {
  return (
    <Badge tone={status === 'ok' ? 'success' : 'danger'} dot>
      {status}
    </Badge>
  )
}
