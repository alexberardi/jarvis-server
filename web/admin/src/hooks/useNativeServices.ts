import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  getNativeServices,
  startNativeService,
  stopNativeService,
  uninstallNativeService,
  getNativeLogs,
  type NativeServicesResponse,
  type NativeLogResponse,
} from '@/api/nativeServices'

const STATUS_KEY = ['native-services'] as const

export function useNativeServices() {
  return useQuery<NativeServicesResponse>({
    queryKey: STATUS_KEY,
    queryFn: getNativeServices,
    // Polling: launchd state can change without us doing anything (KeepAlive
    // restarts, manual launchctl), so we re-poll on a slow loop. 5s matches
    // useContainers.
    staleTime: 5_000,
    refetchInterval: 5_000,
  })
}

export function useStartNativeService() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => startNativeService(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: STATUS_KEY }),
  })
}

export function useStopNativeService() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => stopNativeService(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: STATUS_KEY }),
  })
}

export function useUninstallNativeService() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => uninstallNativeService(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: STATUS_KEY }),
  })
}

export function useNativeLogs(id: string, stream: 'stdout' | 'stderr', enabled: boolean) {
  return useQuery<NativeLogResponse>({
    queryKey: ['native-services-logs', id, stream],
    queryFn: () => getNativeLogs(id, stream, 200),
    enabled,
    staleTime: 3_000,
    refetchInterval: enabled ? 3_000 : false,
  })
}
