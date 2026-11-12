import { apiClient } from './client'

/**
 * A Forge test install on one node (cc's /nodes/{id}/test-install routes, as mobile uses them):
 * the node installs the draft a share code names, keeps it for 20 minutes, and cc records each
 * run of its commands so Send to Forge can report them back to the draft.
 */
const base = (nodeId: string) => `/api/cc/api/v0/nodes/${encodeURIComponent(nodeId)}/test-install`

/** A run of a test-installed command during the node's test window. */
export interface TestRun {
  command: string
  arguments: unknown
  output: unknown
  at: string
}

export interface TestInstall {
  status: 'pending' | 'restarting' | 'completed' | 'failed' | 'expired'
  request_id: string
  package_name: string
  error_message: string | null
  /** What loaded: the draft's commands (chat tools), device protocols and background agents. */
  details: { commands?: string[]; protocols?: string[]; agents?: string[] } | null
  runs?: TestRun[]
  /** What the test install did on the node besides chat tool calls: log lines, scans, device controls. */
  events?: TestEvent[]
}

export interface TestEvent {
  kind: 'log' | 'scan' | 'control' | string
  at?: string
  level?: string
  message?: string
  protocol?: string
  count?: number
  action?: string
  entity_id?: string
  success?: boolean
  error?: string | null
  /** An agent run: the agent, how long it took, whether it worked and how many alerts it raised. */
  agent?: string
  ms?: number
  ok?: boolean
  alert_count?: number
}

/** Share codes are six letters and digits; the Forge shows them in capitals. */
export const normalizeShareCode = (v: string) => v.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 6)

/** storeId is the store the code is looked up in (only that one); cc's default without it. */
export async function requestTestInstall(nodeId: string, shareCode: string, storeId?: string): Promise<string> {
  const { data } = await apiClient.post<{ id: string }>(base(nodeId), {
    share_code: shareCode,
    ...(storeId ? { pantry_store_id: storeId } : {}),
  })
  return data.id
}

export async function pollTestInstall(nodeId: string, requestId: string): Promise<TestInstall> {
  const { data } = await apiClient.get<TestInstall>(`${base(nodeId)}/${encodeURIComponent(requestId)}`)
  return data
}

/** sendToForge has jarvisd send the install result, the runs and the note to the Forge draft. */
/** runTestAgents asks the node to run the draft's background agents now; their runs arrive as events. */
export async function runTestAgents(nodeId: string, requestId: string): Promise<void> {
  await apiClient.post(`${base(nodeId)}/${encodeURIComponent(requestId)}/run-agents`)
}

export async function sendToForge(nodeId: string, requestId: string, note: string): Promise<void> {
  const n = note.trim()
  await apiClient.post(`${base(nodeId)}/${encodeURIComponent(requestId)}/feedback`, n ? { note: n } : {})
}
