import { apiClient } from './client'

/**
 * A node's stored command records (the command-data browser) and the owner's open-tab matches,
 * for interactive lists (docs/webapp/03 step 3). cc checks membership of the node's household
 * for records; tab matches answer only the member who paired the node.
 */
const CC = '/api/cc/api/v0/mobile'

export interface CommandRecord {
  key: string
  data: Record<string, unknown> | null
}

const recordsPath = (nodeId: string, commandName: string) =>
  `${CC}/command-data/nodes/${encodeURIComponent(nodeId)}/commands/${encodeURIComponent(commandName)}/records`

export async function listRecords(nodeId: string, commandName: string): Promise<CommandRecord[]> {
  const { data } = await apiClient.get<{ records: CommandRecord[] }>(recordsPath(nodeId, commandName))
  return data.records ?? []
}

export async function updateRecord(nodeId: string, commandName: string, key: string, patch: Record<string, unknown>): Promise<void> {
  await apiClient.patch(`${recordsPath(nodeId, commandName)}/${encodeURIComponent(key)}`, patch)
}

export interface TabMatch {
  value: string
  title: string
}

export type TabMatches = { ok: true; matches: TabMatch[] } | { ok: false; error: { code: string; message: string } }

/** browserMatches asks the caller's own computer which open tabs match pattern (capture group 1). */
export async function browserMatches(nodeId: string, pattern: string): Promise<TabMatches> {
  const { data } = await apiClient.get<TabMatches>(`${CC}/nodes/${encodeURIComponent(nodeId)}/browser/matches`, { params: { pattern } })
  return data
}
