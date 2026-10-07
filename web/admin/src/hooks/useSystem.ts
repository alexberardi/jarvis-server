import { useQuery } from '@tanstack/react-query'
import { getSystemInfo } from '@/api/system'
import type { SystemInfo } from '@/types/system'

export function useSystemInfo() {
  return useQuery<SystemInfo>({
    queryKey: ['system-info'],
    queryFn: getSystemInfo,
    staleTime: 60_000,
  })
}
