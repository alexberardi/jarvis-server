import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getSetupState, type SetupState } from '@/api/auth'
import { getDoctor, type DoctorReport } from '@/api/doctor'

export const setupKeys = {
  state: ['setup-state'] as const,
  doctor: ['doctor'] as const,
}

/** useSetupState reads /api/setup/state; the superuser fields appear once a session exists. */
export function useSetupState(enabled = true) {
  return useQuery<SetupState>({
    queryKey: setupKeys.state,
    queryFn: () => getSetupState(),
    staleTime: 5_000,
    enabled,
    // While the live model loads, follow it closely; otherwise a slow refresh is enough.
    refetchInterval: (q) => {
      const d = q.state.data
      return d?.superuser && d.models_configured && !d.live_ready ? 3_000 : 30_000
    },
  })
}

export function useDoctor(enabled = true) {
  return useQuery<DoctorReport>({
    queryKey: setupKeys.doctor,
    queryFn: () => getDoctor(),
    staleTime: 30_000,
    enabled,
    retry: false,
  })
}

/** useRerunDoctor runs the checks again (bypassing the 30 s cache) and stores the result. */
export function useRerunDoctor() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => getDoctor(true),
    onSuccess: (data) => qc.setQueryData(setupKeys.doctor, data),
  })
}
