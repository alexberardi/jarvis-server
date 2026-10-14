import { useState, useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'

import { ThemeProvider } from '@/theme/ThemeProvider'
import { AuthProvider, useAuth } from '@/auth/AuthContext'
import AppShell from '@/components/layout/AppShell'
import LoginPage from '@/pages/LoginPage'
import SetupWizard from '@/pages/SetupWizard'
import ChangePasswordPage from '@/pages/ChangePasswordPage'
import { shellRoutes } from '@/routes'
import { getSetupState } from '@/api/auth'

const queryClient = new QueryClient()

function AppRoutes() {
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

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-[var(--color-primary)] border-t-transparent" />
      </div>
    )
  }

  // Once setup signs the new superuser in, the stale flag no longer redirects.
  if (needsSuperuser && !auth.isAuthenticated && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />
  }

  return (
    <Routes>
      <Route path="/setup" element={<SetupWizard needsSuperuser={needsSuperuser} />} />
      <Route path="/change-password" element={<ChangePasswordPage />} />
      <Route path="/login" element={<LoginPage />} />
      <Route element={<AppShell />}>{shellRoutes()}</Route>
    </Routes>
  )
}

export default function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <BrowserRouter>
            <AppRoutes />
          </BrowserRouter>
          <Toaster position="bottom-right" richColors />
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
