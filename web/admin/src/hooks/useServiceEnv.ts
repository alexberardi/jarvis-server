import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { applyServiceEnv, getServiceEnv, updateServiceEnv } from '@/api/serviceEnv'
import type {
  ServiceEnvApplyResponse,
  ServiceEnvResponse,
  ServiceEnvUpdateResponse,
} from '@/api/serviceEnv'

export function useServiceEnv() {
  return useQuery<ServiceEnvResponse>({
    queryKey: ['service-env'],
    queryFn: getServiceEnv,
    staleTime: 30_000,
  })
}

export function useUpdateServiceEnv() {
  const queryClient = useQueryClient()

  return useMutation<
    ServiceEnvUpdateResponse,
    Error,
    { serviceId: string; values: Record<string, string> }
  >({
    mutationFn: ({ serviceId, values }) => updateServiceEnv(serviceId, values),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-env'] })
    },
  })
}

/** Apply saved env by RECREATING the container — docker restart never
 *  re-reads env_file, so restart-based apply silently does nothing. */
export function useApplyServiceEnv() {
  const queryClient = useQueryClient()

  return useMutation<ServiceEnvApplyResponse, Error, string>({
    mutationFn: (serviceId) => applyServiceEnv(serviceId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-env'] })
      queryClient.invalidateQueries({ queryKey: ['containers'] })
    },
  })
}
