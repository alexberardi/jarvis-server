import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import type {
  CatalogResponse,
  HardwareResponse,
  Install,
  InstallRequest,
  InstallResponse,
  InstalledResponse,
  Label,
  LabelUpdate,
  LabelsResponse,
  PromptProvider,
} from '@/api/llm'

/** Query keys, shared so a write can refresh exactly what it changed. */
export const mmKeys = {
  hardware: ['llm', 'hardware'] as const,
  catalog: ['llm', 'catalog'] as const,
  installed: ['llm', 'installed'] as const,
  installs: ['llm', 'installs'] as const,
  labels: ['llm', 'labels'] as const,
  prompt: ['llm', 'prompt-provider'] as const,
  hfToken: ['llm', 'hf-token'] as const,
}

/** How often installs are polled while one is running (06 §5: about once a second). */
export const INSTALL_POLL_MS = 1000

export function useHardware() {
  return useQuery<HardwareResponse>({ queryKey: mmKeys.hardware, queryFn: () => llm.getHardware(), staleTime: 30_000 })
}

export function useRefreshHardware() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => llm.getHardware(true),
    onSuccess: (data) => {
      qc.setQueryData(mmKeys.hardware, data)
      qc.invalidateQueries({ queryKey: mmKeys.catalog })
      qc.invalidateQueries({ queryKey: mmKeys.labels })
    },
  })
}

export function useFetchEngine() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ kind, flavour }: { kind: string; flavour: string }) => llm.fetchEngine(kind, flavour),
    onSuccess: () => qc.invalidateQueries({ queryKey: mmKeys.hardware }),
  })
}

export function useCatalog() {
  return useQuery<CatalogResponse>({ queryKey: mmKeys.catalog, queryFn: llm.getCatalog, staleTime: 15_000 })
}

export function useInstalled() {
  return useQuery<InstalledResponse>({ queryKey: mmKeys.installed, queryFn: llm.listInstalled, staleTime: 5_000 })
}

export function useLabels() {
  return useQuery<LabelsResponse>({
    queryKey: mmKeys.labels,
    queryFn: llm.getLabels,
    staleTime: 3_000,
    // Engines start and swap in the background after a label change.
    refetchInterval: (q) => {
      const labels = q.state.data?.labels ?? []
      const settling = labels.some((l) => !['ready', 'remote', 'not_configured', 'degraded', 'failed'].includes(l.state))
      return settling ? 2_000 : 15_000
    },
  })
}

/** refreshAfterInstall refetches everything an install or a delete can change. */
function refreshModelViews(qc: ReturnType<typeof useQueryClient>) {
  for (const key of [mmKeys.installed, mmKeys.catalog, mmKeys.labels, mmKeys.installs, mmKeys.prompt, mmKeys.hardware]) {
    qc.invalidateQueries({ queryKey: key })
  }
}

/**
 * useInstalls lists recent installs and polls about once a second while any is queued or
 * running. When one finishes, the installed list, catalog, labels and prompt provider refresh.
 */
export function useInstalls() {
  const qc = useQueryClient()
  return useQuery<Install[]>({
    queryKey: mmKeys.installs,
    queryFn: async () => {
      const prev = qc.getQueryData<Install[]>(mmKeys.installs) ?? []
      const next = await llm.listInstalls()
      const wasActive = new Set(prev.filter(llm.isActiveInstall).map((i) => i.id))
      if (next.some((i) => wasActive.has(i.id) && !llm.isActiveInstall(i))) {
        for (const key of [mmKeys.installed, mmKeys.catalog, mmKeys.labels, mmKeys.prompt, mmKeys.hardware]) {
          qc.invalidateQueries({ queryKey: key })
        }
      }
      return next
    },
    refetchInterval: (q) => ((q.state.data ?? []).some(llm.isActiveInstall) ? INSTALL_POLL_MS : 10_000),
  })
}

export function useStartInstall() {
  const qc = useQueryClient()
  return useMutation<InstallResponse, Error, InstallRequest>({
    mutationFn: (req) => llm.startInstall(req),
    onSuccess: (res) => {
      // Show the new install at once; polling takes over from here.
      qc.setQueryData<Install[]>(mmKeys.installs, (old) => [
        res.install,
        ...(old ?? []).filter((i) => i.id !== res.install.id),
      ])
      qc.invalidateQueries({ queryKey: mmKeys.catalog })
    },
  })
}

export function useCancelInstall() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => llm.cancelInstall(id),
    onSuccess: () => refreshModelViews(qc),
  })
}

export function useDeleteModel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, force }: { id: string; force?: boolean }) => llm.deleteModel(id, force),
    onSuccess: () => refreshModelViews(qc),
  })
}

export function useRegisterModel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (req: llm.RegisterRequest) => llm.registerModel(req),
    onSuccess: () => refreshModelViews(qc),
  })
}

export function usePutLabels() {
  const qc = useQueryClient()
  return useMutation<LabelsResponse, Error, Partial<Record<Label, LabelUpdate>>>({
    mutationFn: (body) => llm.putLabels(body),
    onSuccess: (data) => {
      qc.setQueryData(mmKeys.labels, data)
      for (const key of [mmKeys.installed, mmKeys.catalog, mmKeys.prompt]) qc.invalidateQueries({ queryKey: key })
    },
  })
}

export function usePromptProvider() {
  return useQuery<PromptProvider>({ queryKey: mmKeys.prompt, queryFn: llm.getPromptProvider, staleTime: 10_000 })
}

export function useSetPromptProvider() {
  const qc = useQueryClient()
  return useMutation<PromptProvider, Error, string>({
    mutationFn: (value) => llm.setPromptProvider(value),
    onSuccess: (data) => qc.setQueryData(mmKeys.prompt, data),
  })
}

export function useHfTokenSet() {
  return useQuery<boolean>({ queryKey: mmKeys.hfToken, queryFn: llm.hfTokenIsSet, staleTime: 60_000 })
}

export function useSetHfToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (token: string | null) => llm.setHfToken(token),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: mmKeys.hfToken })
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
  })
}
