import { Link, Outlet } from 'react-router-dom'
import { ShieldAlert } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'

/**
 * RequireSuperuser wraps the Admin pages (webapp 01 §4.1). A member following an admin link
 * gets a notice instead of a page whose calls would all fail: the server refuses them anyway
 * (403, the gateway tiers, G1). This is presentation, never the security boundary.
 */
export default function RequireSuperuser() {
  const { state } = useAuth()
  if (state.user?.is_superuser) return <Outlet />
  return <AdminOnlyNotice />
}

export function AdminOnlyNotice() {
  return (
    <div className="flex min-h-[60vh] flex-col items-center justify-center px-4 text-center">
      <ShieldAlert size={40} className="mb-3 text-[var(--color-text-muted)]" aria-hidden="true" />
      <h1 className="mb-2 text-xl font-semibold text-[var(--color-text)]">This page is for the install's administrator</h1>
      <p className="mb-4 max-w-md text-sm text-[var(--color-text-muted)]">
        Your account can chat with Jarvis and manage your own devices. Ask whoever set up Jarvis Automation
        if you need something changed here.
      </p>
      <Link to="/chat" className="text-[var(--color-primary)] underline hover:opacity-80">
        Go to Chat
      </Link>
    </div>
  )
}
