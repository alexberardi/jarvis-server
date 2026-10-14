import { useQuery } from '@tanstack/react-query'
import { getSystemInfo } from '@/api/system'
import type { SystemInfo } from '@/types/system'

/** useSystemInfo is superuser-only on the server; members pass enabled=false (no 403 noise). */
export function useSystemInfo(enabled = true) {
  return useQuery<SystemInfo>({
    queryKey: ['system-info'],
    queryFn: getSystemInfo,
    staleTime: 60_000,
    enabled,
  })
}
