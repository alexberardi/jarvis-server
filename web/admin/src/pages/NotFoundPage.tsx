import { Link } from 'react-router-dom'

export default function NotFoundPage() {
  return (
    <div className="flex min-h-[60vh] flex-col items-center justify-center">
      <h1 className="mb-2 text-4xl font-bold text-[var(--color-text)]">404</h1>
      <p className="mb-4 text-[var(--color-text-muted)]">Page not found</p>
      <Link
        to="/settings"
        className="text-[var(--color-primary)] underline hover:opacity-80"
      >
        Go to Settings
      </Link>
    </div>
  )
}
