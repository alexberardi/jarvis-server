import { useState, useEffect } from 'react'
import { BrowserRouter, Routes, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'

import { ThemeProvider } from '@/theme/ThemeProvider'
import { AuthProvider, useAuth } from '@/auth/AuthContext'
import { appRoutes } from '@/routes'
import SessionCacheGuard from '@/auth/SessionCacheGuard'
import { getSetupState } from '@/api/auth'

const queryClient = new QueryClient()

export function AppRoutes() {
  const [checking, setChecking] = useState(true)
  const [needsSuperuser, setNeedsSuperuser] = useState(false)
  const location = useLocation()
  const { state: auth } = useAuth()

  // Boot gate (S1): while no superuser exists, everything leads to first-run setup.
  useEffect(() => {
    getSetupState()
      .then((s) => setNeedsSuperuser(s.needs_superuser))
      .catch(() => setNeedsSuperuser(false))
      .finally(() => setChecking(false))
  }, [])

  // Someone is signed in, so the install has its superuser: the boot flag is stale from here
  // on (signing out after setup must reach the login page, not bounce back into the wizard).
  if (needsSuperuser && auth.isAuthenticated) setNeedsSuperuser(false)

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-[var(--color-primary)] border-t-transparent" />
      </div>
    )
  }

  if (needsSuperuser && !auth.isAuthenticated && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />
  }

  return <Routes>{appRoutes(needsSuperuser)}</Routes>
}

export default function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <SessionCacheGuard />
          <BrowserRouter>
            <AppRoutes />
          </BrowserRouter>
          <Toaster position="bottom-right" richColors />
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
