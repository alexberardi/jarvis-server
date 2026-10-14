import type { ReactNode } from 'react'
import { Navigate, Route } from 'react-router-dom'

import RequireSuperuser from '@/auth/RequireSuperuser'
import ToAdmin from '@/components/layout/ToAdmin'
import AccountPage from '@/pages/AccountPage'
import ChatPage from '@/pages/ChatPage'
import ConnectionsPage from '@/pages/ConnectionsPage'
import DashboardPage from '@/pages/DashboardPage'
import HomeRedirect from '@/pages/HomeRedirect'
import LogsPage from '@/pages/LogsPage'
import ModelsPage from '@/pages/ModelsPage'
import NodesPage from '@/pages/NodesPage'
import NotFoundPage from '@/pages/NotFoundPage'
import SettingsPage from '@/pages/SettingsPage'
import TraceDetailPage from '@/pages/TraceDetailPage'
import TracesPage from '@/pages/TracesPage'
import UpdatePage from '@/pages/UpdatePage'
import UsersPage from '@/pages/UsersPage'

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

/** shellRoutes are the pages inside the app shell: member pages at the root, admin under /admin. */
export function shellRoutes() {
  return (
    <>
      <Route path="/" element={<HomeRedirect />} />
      <Route path="/chat" element={<ChatPage />} />
      <Route path="/account" element={<AccountPage />} />
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

