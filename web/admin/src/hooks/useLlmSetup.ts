import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getLlmStatus, configureLlm, downloadModel } from '@/api/llmSetup'
import type { LlmStatus, ConfigureResponse, DownloadResponse } from '@/api/llmSetup'

export function useLlmStatus() {
  return useQuery<LlmStatus>({
    queryKey: ['llm-status'],
    queryFn: getLlmStatus,
    staleTime: 30_000,
    retry: 1,
  })
}

export function useConfigureLlm() {
  const queryClient = useQueryClient()

  return useMutation<ConfigureResponse, Error, Record<string, unknown>>({
    mutationFn: (settings) => configureLlm(settings),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['llm-status'] })
      queryClient.invalidateQueries({ queryKey: ['containers'] })
    },
  })
}

export function useDownloadModel() {
  return useMutation<
    DownloadResponse,
    Error,
    { repo: string; filename?: string; token?: string }
  >({
    mutationFn: ({ repo, filename, token }) => downloadModel(repo, filename, token),
  })
}
