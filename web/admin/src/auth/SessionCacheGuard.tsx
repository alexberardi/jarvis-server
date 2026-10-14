import { useEffect, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/hooks/useAuth'

/**
 * SessionCacheGuard drops every cached query when the signed-in user changes or signs out
 * (webapp 01 G13): the last person's inbox, nodes and chat capabilities must not flash up for
 * the next one at a shared computer.
 */
export default function SessionCacheGuard() {
  const qc = useQueryClient()
  const { state } = useAuth()
  const id = state.user?.id ?? null
  const last = useRef(id)
  useEffect(() => {
    if (last.current !== id) qc.clear()
    last.current = id
  }, [id, qc])
  return null
}
