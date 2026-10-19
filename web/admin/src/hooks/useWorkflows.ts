import { useQuery } from '@tanstack/react-query'
import { getRecording, getWorkflow, listRecordings, listWorkflows } from '@/api/workflows'
import { useAuth } from '@/hooks/useAuth'

/** Keys include the household where the call is scoped to it (the active household's token). */
export const workflowKeys = {
  list: (hh: string) => ['workflows', hh, 'list'] as const,
  item: (id: string) => ['workflows', 'item', id] as const,
  recordings: (hh: string) => ['recordings', hh, 'list'] as const,
  recording: (id: string) => ['recordings', 'item', id] as const,
}

export function useWorkflows() {
  const { activeHouseholdId: hh } = useAuth()
  return useQuery({ queryKey: workflowKeys.list(hh), queryFn: () => listWorkflows(hh), enabled: !!hh, retry: false })
}

export function useWorkflow(id: string) {
  return useQuery({ queryKey: workflowKeys.item(id), queryFn: () => getWorkflow(id), retry: false })
}

export function useRecordings() {
  const { activeHouseholdId: hh } = useAuth()
  return useQuery({ queryKey: workflowKeys.recordings(hh), queryFn: () => listRecordings(hh), enabled: !!hh, retry: false })
}

export function useRecording(id: string | null) {
  return useQuery({ queryKey: workflowKeys.recording(id ?? ''), queryFn: () => getRecording(id!), enabled: !!id, retry: false })
}
