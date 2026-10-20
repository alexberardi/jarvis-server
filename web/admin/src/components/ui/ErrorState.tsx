import { useState, type ReactNode } from 'react'
import { AlertTriangle, RotateCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import Button from './Button'

interface ErrorStateProps {
  title?: ReactNode
  /** What went wrong, in words the user can act on (lib/errors' message, not a stack). */
  message?: ReactNode
  /** Shows a "Try again" button. A returned promise keeps it busy until it settles. */
  onRetry?: () => void | Promise<unknown>
  className?: string
}

/** ErrorState replaces content that failed to load, and offers to load it again. */
export default function ErrorState({ title = "Couldn't load this", message, onRetry, className }: ErrorStateProps) {
  const [retrying, setRetrying] = useState(false)
  const retry = async () => {
    if (!onRetry) return
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }
  return (
    <div role="alert" className={cn('flex flex-col items-center px-4 py-10 text-center', className)}>
      <span aria-hidden className="mb-3 flex size-10 items-center justify-center rounded-pill bg-danger-soft text-danger">
        <AlertTriangle size={20} />
      </span>
      <p className="text-subheading text-text">{title}</p>
      {message && <p className="mt-1 max-w-sm text-small break-words text-text-muted">{message}</p>}
      {onRetry && (
        <Button className="mt-4" icon={<RotateCw size={16} aria-hidden />} loading={retrying} onClick={retry}>
          Try again
        </Button>
      )}
    </div>
  )
}
