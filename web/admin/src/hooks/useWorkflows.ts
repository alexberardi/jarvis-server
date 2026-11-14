import { useQuery } from '@tanstack/react-query'
import { getLastRun, getRecording, getWorkflow, listRecordings, listWorkflows } from '@/api/workflows'
import { useAuth } from '@/hooks/useAuth'

/** Keys include the household where the call is scoped to it (the active household's token). */
export const workflowKeys = {
  list: (hh: string) => ['workflows', hh, 'list'] as const,
  item: (id: string) => ['workflows', 'item', id] as const,
  lastRun: (id: string) => ['workflows', 'item', id, 'last-run'] as const,
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

/**
 * useLastRun follows a workflow's last run: every 2 s while it is going, every 5 s while it waits
 * on its owner, and not at all once it has ended. It is the owner's alone (403 for anyone else).
 */
export function useLastRun(id: string, enabled: boolean) {
  return useQuery({
    queryKey: workflowKeys.lastRun(id),
    queryFn: () => getLastRun(id),
    enabled,
    retry: false,
    refetchInterval: (q) => (q.state.data?.state === 'running' ? 2000 : q.state.data?.state === 'waiting' ? 5000 : false),
  })
}

export function useRecordings() {
  const { activeHouseholdId: hh } = useAuth()
  return useQuery({ queryKey: workflowKeys.recordings(hh), queryFn: () => listRecordings(hh), enabled: !!hh, retry: false })
}

export function useRecording(id: string | null) {
  return useQuery({ queryKey: workflowKeys.recording(id ?? ''), queryFn: () => getRecording(id!), enabled: !!id, retry: false })
}
