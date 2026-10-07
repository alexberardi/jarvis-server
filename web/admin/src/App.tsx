import { useState, useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'

import { ThemeProvider } from '@/theme/ThemeProvider'
import { AuthProvider } from '@/auth/AuthContext'
import AppShell from '@/components/layout/AppShell'
import LoginPage from '@/pages/LoginPage'
import SettingsPage from '@/pages/SettingsPage'
import ServicesPage from '@/pages/ServicesPage'
import DashboardPage from '@/pages/DashboardPage'
import NodesPage from '@/pages/NodesPage'
import UsersPage from '@/pages/UsersPage'
import NativeServicesPage from '@/pages/NativeServicesPage'
import ModelsPage from '@/pages/ModelsPage'
import QuickSetsPage from '@/pages/QuickSetsPage'
import UpdatePage from '@/pages/UpdatePage'
import ReconcilePage from '@/pages/ReconcilePage'
import TracesPage from '@/pages/TracesPage'
import TraceDetailPage from '@/pages/TraceDetailPage'
import NotFoundPage from '@/pages/NotFoundPage'
import SetupWizard from '@/pages/SetupWizard'
import LlmSetupWizard from '@/pages/LlmSetupWizard'
import { getInstallStatus } from '@/api/install'

const queryClient = new QueryClient()

function AppRoutes() {
  const [checking, setChecking] = useState(true)
  const [needsInstall, setNeedsInstall] = useState(false)
  const [deployedNeedsAccount, setDeployedNeedsAccount] = useState(false)

  useEffect(() => {
    getInstallStatus()
      .then((status) => {
        if (status.state === 'deployed-needs-account') {
          // Compose-export mode: skip to account creation
          setDeployedNeedsAccount(true)
          setNeedsInstall(true)
        } else {
          setNeedsInstall(!status.configured)
        }
      })
      .catch(() => {
        // If we can't reach the backend, don't force install wizard
        setNeedsInstall(false)
      })
      .finally(() => setChecking(false))
  }, [])

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-background)]">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-[var(--color-primary)] border-t-transparent" />
      </div>
    )
  }

  return (
    <Routes>
      <Route path="/setup" element={<SetupWizard skipToAccount={deployedNeedsAccount} />} />
      <Route path="/login" element={<LoginPage />} />
      <Route path="/llm-setup" element={<LlmSetupWizard />} />
      <Route element={<AppShell />}>
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/services" element={<ServicesPage />} />
        <Route path="/models" element={<ModelsPage />} />
        <Route path="/quick-sets" element={<QuickSetsPage />} />
        <Route path="/traces" element={<TracesPage />} />
        <Route path="/traces/:id" element={<TraceDetailPage />} />
        <Route path="/update" element={<UpdatePage />} />
        <Route path="/reconcile" element={<ReconcilePage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/native-services" element={<NativeServicesPage />} />
        <Route
          path="/"
          element={
            needsInstall
              ? <Navigate to="/setup" replace />
              : <Navigate to="/dashboard" replace />
          }
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
