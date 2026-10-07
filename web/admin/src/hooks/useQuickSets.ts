import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getQuickSets, applyQuickSet, createCustomPreset, deleteCustomPreset } from '@/api/quickSets'
import type {
  QuickSetsResponse,
  ApplyQuickSetRequest,
  ApplyQuickSetResponse,
  CreateCustomPresetRequest,
  QuickSetPreset,
} from '@/api/quickSets'

export function useQuickSets() {
  return useQuery<QuickSetsResponse>({
    queryKey: ['quick-sets'],
    queryFn: getQuickSets,
    staleTime: 60_000,
  })
}

export function useApplyQuickSet() {
  const queryClient = useQueryClient()

  return useMutation<ApplyQuickSetResponse, Error, ApplyQuickSetRequest>({
    mutationFn: (req) => applyQuickSet(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['quick-sets'] })
      queryClient.invalidateQueries({ queryKey: ['settings'] })
      queryClient.invalidateQueries({ queryKey: ['llm-status'] })
      queryClient.invalidateQueries({ queryKey: ['containers'] })
    },
  })
}

export function useCreateCustomPreset() {
  const queryClient = useQueryClient()

  return useMutation<{ preset: QuickSetPreset }, Error, CreateCustomPresetRequest>({
    mutationFn: (req) => createCustomPreset(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['quick-sets'] })
    },
  })
}

export function useDeleteCustomPreset() {
  const queryClient = useQueryClient()

  return useMutation<{ success: boolean }, Error, string>({
    mutationFn: (id) => deleteCustomPreset(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['quick-sets'] })
    },
  })
}
