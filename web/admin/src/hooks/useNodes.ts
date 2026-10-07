import { useQuery } from '@tanstack/react-query'
import { getNodeLiveness, getNodesView } from '@/api/nodes'
import type { NodeLiveness, NodesView } from '@/api/nodes'

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
