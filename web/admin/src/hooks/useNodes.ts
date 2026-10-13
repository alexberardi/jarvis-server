import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { approvePairing, denyPairing, getNodeLiveness, getNodesView, getPairings } from '@/api/nodes'
import type { ApprovePairing, NodeLiveness, NodesView, PairingsView } from '@/api/nodes'

export function useNodesView() {
  return useQuery<NodesView>({
    queryKey: ['admin-nodes-view'],
    queryFn: getNodesView,
    staleTime: 30_000,
  })
}

/** useNodeLiveness is optional decoration: the page still renders when cc is unavailable. */
export function useNodeLiveness() {
  return useQuery<NodeLiveness[]>({
    queryKey: ['node-liveness'],
    queryFn: getNodeLiveness,
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: false,
  })
}

/** usePairings polls the computers waiting for approval, so a new code shows up within seconds. */
export function usePairings() {
  return useQuery<PairingsView>({
    queryKey: ['node-pairings'],
    queryFn: getPairings,
    refetchInterval: 3_000,
    retry: false,
  })
}

function useInvalidatePairings() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['node-pairings'] })
    void qc.invalidateQueries({ queryKey: ['admin-nodes-view'] })
    void qc.invalidateQueries({ queryKey: ['node-liveness'] })
  }
}

export function useApprovePairing() {
  const invalidate = useInvalidatePairings()
  return useMutation({ mutationFn: (b: ApprovePairing) => approvePairing(b), onSettled: invalidate })
}

export function useDenyPairing() {
  const invalidate = useInvalidatePairings()
  return useMutation({ mutationFn: (code: string) => denyPairing(code), onSettled: invalidate })
}
