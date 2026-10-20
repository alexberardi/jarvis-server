import { lazy, Suspense, type ReactNode } from 'react'
import { Navigate, Route } from 'react-router-dom'

import RequireSuperuser from '@/auth/RequireSuperuser'
import AppShell from '@/components/layout/AppShell'
import ToAdmin from '@/components/layout/ToAdmin'
import AccountPage from '@/pages/AccountPage'
import AddonsPage from '@/pages/AddonsPage'
import ChangePasswordPage from '@/pages/ChangePasswordPage'
import ChatPage from '@/pages/ChatPage'
import ConnectionsPage from '@/pages/ConnectionsPage'
import DashboardPage from '@/pages/DashboardPage'
import HomeRedirect from '@/pages/HomeRedirect'
import InboxItemPage from '@/pages/InboxItemPage'
import InboxPage from '@/pages/InboxPage'
import LoginPage from '@/pages/LoginPage'
import LogsPage from '@/pages/LogsPage'
import ModelsPage from '@/pages/ModelsPage'
import NodesPage from '@/pages/NodesPage'
import NotFoundPage from '@/pages/NotFoundPage'
import RecordingsPage from '@/pages/RecordingsPage'
import SettingsPage from '@/pages/SettingsPage'
import SetupWizard from '@/pages/SetupWizard'
import TraceDetailPage from '@/pages/TraceDetailPage'
import TracesPage from '@/pages/TracesPage'
import UpdatePage from '@/pages/UpdatePage'
import UsersPage from '@/pages/UsersPage'
import WorkflowEditPage from '@/pages/WorkflowEditPage'
import WorkflowsPage from '@/pages/WorkflowsPage'

/**
 * The design-system gallery (pages/dev/UiGalleryPage) is dev-only: there under `vite dev` and in
 * a build made with VITE_UI_GALLERY=1 (scripts/sandbox up --ui, for its screenshots). A release
 * build leaves the route and the page's chunk out.
 */
const UiGalleryPage =
  import.meta.env.DEV || import.meta.env.VITE_UI_GALLERY === '1' ? lazy(() => import('@/pages/dev/UiGalleryPage')) : null

/**
 * The Admin pages (webapp 01 §4.2), under /admin/. Their old top-level paths redirect, so
 * bookmarks and links in docs keep working.
 */
const adminPages: { path: string; element: ReactNode }[] = [
  { path: 'dashboard', element: <DashboardPage /> },
  { path: 'models', element: <ModelsPage /> },
  { path: 'settings', element: <SettingsPage /> },
  { path: 'connections', element: <ConnectionsPage /> },
  { path: 'logs', element: <LogsPage /> },
  { path: 'traces', element: <TracesPage /> },
  { path: 'traces/:id', element: <TraceDetailPage /> },
  { path: 'nodes', element: <NodesPage /> },
  { path: 'users', element: <UsersPage /> },
  { path: 'update', element: <UpdatePage /> },
]

/**
 * appRoutes is the whole route tree: the pages outside the app shell, then the shell's.
 * e2e/coverage.json maps each of its paths to the E2E tests covering it (coverage.test.tsx).
 */
export function appRoutes(needsSuperuser = false) {
  return (
    <>
      <Route path="/setup" element={<SetupWizard needsSuperuser={needsSuperuser} />} />
      <Route path="/change-password" element={<ChangePasswordPage />} />
      <Route path="/login" element={<LoginPage />} />
      <Route element={<AppShell />}>{shellRoutes()}</Route>
    </>
  )
}

/** shellRoutes are the pages inside the app shell: member pages at the root, admin under /admin. */
export function shellRoutes() {
  return (
    <>
      <Route path="/" element={<HomeRedirect />} />
      <Route path="/chat" element={<ChatPage />} />
      <Route path="/addons" element={<AddonsPage />} />
      <Route path="/inbox" element={<InboxPage />} />
      <Route path="/inbox/:id" element={<InboxItemPage />} />
      <Route path="/workflows" element={<WorkflowsPage />} />
      <Route path="/workflows/:id" element={<WorkflowEditPage />} />
      <Route path="/recordings" element={<RecordingsPage />} />
      <Route path="/account" element={<AccountPage />} />
      {UiGalleryPage && (
        <Route
          path="/dev/ui"
          element={
            <Suspense fallback={null}>
              <UiGalleryPage />
            </Suspense>
          }
        />
      )}
      <Route path="/admin" element={<RequireSuperuser />}>
        <Route index element={<Navigate to="/admin/dashboard" replace />} />
        {adminPages.map((p) => (
          <Route key={p.path} path={p.path} element={p.element} />
        ))}
        <Route path="services" element={<Navigate to="/admin/connections" replace />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
      {adminPages.map((p) => (
        <Route key={p.path} path={'/' + p.path} element={<ToAdmin />} />
      ))}
      <Route path="/services" element={<ToAdmin to="/admin/connections" />} />
      <Route path="*" element={<NotFoundPage />} />
    </>
  )
}

