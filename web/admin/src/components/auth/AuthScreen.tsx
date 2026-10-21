import type { ReactNode } from 'react'
import { Card, Spinner } from '@/components/ui'
import { cn } from '@/lib/utils'

/** The product's name over every screen outside the app shell (sign-in, password, setup, boot). */
export function Wordmark({ className }: { className?: string }) {
  return <p className={cn('text-subheading text-primary', className)}>Jarvis Automation</p>
}

interface AuthScreenProps {
  title: ReactNode
  /** A line under the title: why the person is here, or what happens next. */
  description?: ReactNode
  children: ReactNode
}

/**
 * AuthScreen is a page outside the app shell with one short task (sign in, change a password):
 * the wordmark, then a card with the page's h1 and its form. A full-width column on a phone,
 * 384px from sm up.
 */
export default function AuthScreen({ title, description, children }: AuthScreenProps) {
  return (
    <main className="flex min-h-dvh flex-col items-center bg-background px-4 py-10 sm:justify-center">
      <div className="w-full max-w-sm">
        <Wordmark className="mb-4 text-center" />
        <Card className="md:p-6">
          <h1 className="text-title text-balance text-text">{title}</h1>
          {description && <p className="mt-1 text-body text-text-muted">{description}</p>}
          <div className="mt-6">{children}</div>
        </Card>
      </div>
    </main>
  )
}

/** AuthLoading stands in for a screen while the install is asked where to go, and says so. */
export function AuthLoading({ label }: { label: string }) {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-4 bg-background px-4">
      <Wordmark />
      <p className="flex items-center gap-2 text-small text-text-muted">
        <Spinner label={label} />
        <span aria-hidden>{label}</span>
      </p>
    </main>
  )
}
