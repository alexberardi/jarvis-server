import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { checkNow, getUpdate, setUpdatesEnabled, type UpdateInfo } from '@/api/update'

export const updateKey = ['update'] as const

/** useUpdateCheck reads the cached verdict; it never makes jarvisd contact GitHub when off (I2). */
export function useUpdateCheck() {
  return useQuery<UpdateInfo>({
    queryKey: updateKey,
    queryFn: () => getUpdate(),
    staleTime: 5 * 60 * 1000,
    retry: 1,
    refetchOnWindowFocus: false,
  })
}

export function useCheckNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => checkNow(),
    onSuccess: (data) => qc.setQueryData(updateKey, data),
  })
}

export function useSetUpdatesEnabled() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (enabled: boolean) => setUpdatesEnabled(enabled),
    onSuccess: (data) => {
      qc.setQueryData(updateKey, data)
      void qc.invalidateQueries({ queryKey: ['settings'] })
    },
  })
}
