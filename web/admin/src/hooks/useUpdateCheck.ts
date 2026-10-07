import { useQuery } from '@tanstack/react-query'
import { getUpdateInfo } from '@/api/update'
import type { UpdateInfo } from '@/api/update'

export function useUpdateCheck() {
  return useQuery<UpdateInfo>({
    queryKey: ['update-check'],
    queryFn: getUpdateInfo,
    staleTime: 5 * 60 * 1000, // 5 minutes
    retry: 1,
    refetchOnWindowFocus: false,
  })
}
