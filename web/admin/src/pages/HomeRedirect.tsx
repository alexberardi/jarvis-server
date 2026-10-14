import { Navigate } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { useSetupState } from '@/hooks/useSetup'

/**
 * HomeRedirect is `/` (webapp 01 §4.1): Chat is home. A superuser lands on the dashboard
 * instead while setup is unfinished or the live model isn't ready (the dashboard's red banner).
 */
export default function HomeRedirect() {
  const { state } = useAuth()
  const superuser = Boolean(state.user?.is_superuser)
  const setup = useSetupState(superuser)
  if (!superuser) return <Navigate to="/chat" replace />
  if (setup.isLoading) return null
  const d = setup.data
  const needsAttention = !!d && (d.setup_completed === false || d.live_ready === false)
  return <Navigate to={needsAttention ? '/admin/dashboard' : '/chat'} replace />
}
