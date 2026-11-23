import { useQuery } from '@tanstack/react-query'
import { listChatNodes } from '@/api/chat'
import { deviceNodeName, getDeviceConfig, type DeviceNode } from '@/api/devices'

/**
 * useRoutineNodes is the household's nodes for running routines: their names and the primary
 * node (smart-home config) and whether each can run Simple routines (its capabilities).
 */
export function useRoutineNodes(hh: string) {
  const config = useQuery({ queryKey: ['routines', hh, 'node-config'], queryFn: ({ signal }) => getDeviceConfig(hh, signal), staleTime: 30_000 })
  const caps = useQuery({ queryKey: ['routines', hh, 'node-caps'], queryFn: () => listChatNodes(hh), staleTime: 30_000 })
  const nodes: DeviceNode[] = config.data?.nodes ?? []
  const simpleOn = (id: string) => caps.data?.some((n) => n.node_id === id && n.capabilities?.simple_routines === true) === true
  return {
    nodes,
    primary: config.data?.primary_node_id || null,
    simpleOn,
    capsKnown: caps.isSuccess,
    label: (id: string | null | undefined) => {
      const n = nodes.find((x) => x.node_id === id)
      return n ? deviceNodeName(n) : (id ?? '')
    },
  }
}
