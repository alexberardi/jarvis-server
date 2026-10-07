import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getInstalledModels, downloadModel, deleteModel } from '@/api/models'
import type { DownloadRequest } from '@/api/models'

export function useInstalledModels() {
  return useQuery({
    queryKey: ['models', 'installed'],
    queryFn: getInstalledModels,
    staleTime: 10_000,
  })
}

export function useDownloadModel() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (req: DownloadRequest) => downloadModel(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['models', 'installed'] })
    },
  })
}

export function useDeleteModel() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => deleteModel(name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['models', 'installed'] })
    },
  })
}
