import { useState, useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'

import { ThemeProvider } from '@/theme/ThemeProvider'
import { AuthProvider, useAuth } from '@/auth/AuthContext'
import AppShell from '@/components/layout/AppShell'
import LoginPage from '@/pages/LoginPage'
import SettingsPage from '@/pages/SettingsPage'
import ServicesPage from '@/pages/ServicesPage'
import DashboardPage from '@/pages/DashboardPage'
import NodesPage from '@/pages/NodesPage'
import UsersPage from '@/pages/UsersPage'
import NativeServicesPage from '@/pages/NativeServicesPage'
import ModelsPage from '@/pages/ModelsPage'
import UpdatePage from '@/pages/UpdatePage'
import ReconcilePage from '@/pages/ReconcilePage'
import TracesPage from '@/pages/TracesPage'
import TraceDetailPage from '@/pages/TraceDetailPage'
import NotFoundPage from '@/pages/NotFoundPage'
import SetupWizard from '@/pages/SetupWizard'
import ChangePasswordPage from '@/pages/ChangePasswordPage'
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
      <Route element={<AppShell />}>
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/services" element={<ServicesPage />} />
        <Route path="/models" element={<ModelsPage />} />
        <Route path="/traces" element={<TracesPage />} />
        <Route path="/traces/:id" element={<TraceDetailPage />} />
        <Route path="/update" element={<UpdatePage />} />
        <Route path="/reconcile" element={<ReconcilePage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/native-services" element={<NativeServicesPage />} />
        <Route
          path="/"
          element={<Navigate to="/dashboard" replace />}
        />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
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
