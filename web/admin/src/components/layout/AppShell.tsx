import { useEffect, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import Sidebar from './Sidebar'
import Header from './Header'

export default function AppShell() {
  const { state, mustChangePassword } = useAuth()
  const { pathname } = useLocation()
  // The drawer is open for the page it was opened on, so any navigation (a link, back/forward)
  // closes it without an effect.
  const [navOpenOn, setNavOpenOn] = useState<string | null>(null)
  const navOpen = navOpenOn === pathname
  const closeNav = () => setNavOpenOn(null)

  // Escape closes the drawer.
  useEffect(() => {
    if (!navOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setNavOpenOn(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navOpen])

  if (state.isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <p className="text-[var(--color-text-muted)]">Loading...</p>
      </div>
    )
  }

  if (!state.isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  // A temporary password opens nothing but the change-password screen (O4).
  if (mustChangePassword) {
    return <Navigate to="/change-password" replace />
  }

  return (
    <div className="flex h-dvh bg-[var(--color-background)]">
      <Sidebar open={navOpen} onClose={closeNav} />
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <Header onOpenNav={() => setNavOpenOn(pathname)} navOpen={navOpen} />
        <main className="min-w-0 flex-1 overflow-y-auto p-4 md:p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
