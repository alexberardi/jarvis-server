import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getHouseholds, getHouseholdNodes, trainNodeAdapter } from '@/api/nodes'
import type { Household, HouseholdNode, TrainAdapterResponse } from '@/types/nodes'

export function useHouseholds() {
  return useQuery<Household[]>({
    queryKey: ['households'],
    queryFn: getHouseholds,
    staleTime: 30_000,
  })
}

export function useHouseholdNodes(householdId: string | null) {
  return useQuery<HouseholdNode[]>({
    queryKey: ['household-nodes', householdId],
    queryFn: () => getHouseholdNodes(householdId!),
    enabled: !!householdId,
    staleTime: 30_000,
  })
}

export function useTrainAdapter() {
  const queryClient = useQueryClient()

  return useMutation<TrainAdapterResponse, Error, string>({
    mutationFn: trainNodeAdapter,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['household-nodes'] })
    },
  })
}
