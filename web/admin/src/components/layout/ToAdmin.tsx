import { Navigate, useLocation } from 'react-router-dom'

/**
 * ToAdmin redirects an old top-level admin path (/dashboard, /traces/abc) to its home under
 * /admin (webapp 01 §4.2), keeping the query and hash; `to` names a different target.
 */
export default function ToAdmin({ to }: { to?: string }) {
  const { pathname, search, hash } = useLocation()
  return <Navigate to={(to ?? '/admin' + pathname) + search + hash} replace />
}
