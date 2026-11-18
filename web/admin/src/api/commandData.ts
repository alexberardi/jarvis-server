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

/** updateRecord overlays patch on the record; cc reads it from {patch}, and the node keeps only editable fields. */
export async function updateRecord(nodeId: string, commandName: string, key: string, patch: Record<string, unknown>): Promise<void> {
  await apiClient.patch(`${recordsPath(nodeId, commandName)}/${encodeURIComponent(key)}`, { patch })
}

/** One field of a command's records, as the command describes it (IJarvisCommand's editable_fields). */
export interface FieldSpec {
  name: string
  type: string
  label?: string
  description?: string
  editable?: boolean
  create_only?: boolean
  required?: boolean
  enum_values?: string[]
  item_type?: string
  placeholder?: string
}

/** mode: enabled (editable), readonly, or disabled (the command keeps no browsable data). */
export interface CommandSchema {
  mode: string
  supports_create?: boolean
  fields: FieldSpec[]
}

export interface CommandSummary {
  command_name: string
  mode: string
  storage_name: string
}

export interface BrowsedRecord {
  key: string
  summary?: { title: string; subtitle: string | null; icon?: string }
  data: Record<string, unknown> | null
}

const commandsPath = (nodeId: string) => `${CC}/command-data/nodes/${encodeURIComponent(nodeId)}/commands`

export async function listCommands(nodeId: string): Promise<CommandSummary[]> {
  return (await apiClient.get<{ commands: CommandSummary[] }>(commandsPath(nodeId))).data.commands ?? []
}

export async function getSchema(nodeId: string, commandName: string): Promise<CommandSchema> {
  return (await apiClient.get<CommandSchema>(`${commandsPath(nodeId)}/${encodeURIComponent(commandName)}/schema`)).data
}

/** browseRecords is the command's records with the node's summaries (at most 200; truncated says so). */
export async function browseRecords(nodeId: string, commandName: string): Promise<{ records: BrowsedRecord[]; truncated: boolean; count: number }> {
  return (await apiClient.get(recordsPath(nodeId, commandName))).data
}

export async function getRecord(nodeId: string, commandName: string, key: string): Promise<{ record: Record<string, unknown>; schema: CommandSchema }> {
  return (await apiClient.get(`${recordsPath(nodeId, commandName)}/${encodeURIComponent(key)}`)).data
}

/** createRecord sends only field values: the node mints the key and stamps the owner. */
export async function createRecord(nodeId: string, commandName: string, data: Record<string, unknown>): Promise<{ key: string }> {
  return (await apiClient.post(recordsPath(nodeId, commandName), { data })).data
}

export async function deleteRecord(nodeId: string, commandName: string, key: string): Promise<void> {
  await apiClient.delete(`${recordsPath(nodeId, commandName)}/${encodeURIComponent(key)}`)
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
