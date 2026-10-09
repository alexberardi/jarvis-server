import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getAllSettings, resetHouseholdSetting, updateSetting } from '@/api/settings'
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
    { serviceName: string; key: string; value: unknown; householdId?: string }
  >({
    mutationFn: ({ serviceName, key, value, householdId }) =>
      updateSetting(serviceName, key, value, householdId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings'] })
    },
  })
}

/** "Use default": remove a household's own value. */
export function useResetHouseholdSetting() {
  const queryClient = useQueryClient()

  return useMutation<
    ServiceUpdateResponse,
    Error,
    { serviceName: string; key: string; householdId: string }
  >({
    mutationFn: ({ serviceName, key, householdId }) => resetHouseholdSetting(serviceName, key, householdId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings'] })
    },
  })
}
