import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getAllSettings, updateSetting } from '@/api/settings'
import type { AggregatedSettingsResponse, ServiceUpdateResponse } from '@/types/settings'

export function useAllSettings() {
  return useQuery<AggregatedSettingsResponse>({
    queryKey: ['settings'],
    queryFn: getAllSettings,
    staleTime: 30_000,
  })
}

export function useUpdateSetting() {
  const queryClient = useQueryClient()

  return useMutation<
    ServiceUpdateResponse,
    Error,
    { serviceName: string; key: string; value: unknown }
  >({
    mutationFn: ({ serviceName, key, value }) => updateSetting(serviceName, key, value),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings'] })
    },
  })
}
