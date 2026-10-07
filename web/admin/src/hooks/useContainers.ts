import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getContainers, restartContainer } from '@/api/containers'
import type { ContainersResponse } from '@/types/containers'

export function useContainers() {
  return useQuery<ContainersResponse>({
    queryKey: ['containers'],
    queryFn: getContainers,
    staleTime: 10_000,
    refetchInterval: 10_000,
  })
}

export function useRestartContainer() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (id: string) => restartContainer(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['containers'] })
    },
  })
}
