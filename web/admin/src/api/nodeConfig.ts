import { apiClient } from './client'

export interface ConfigField {
  key: string
  label: string
  help?: string
  group: string
  type: 'boolean' | 'number' | 'time'
  min: number
  max: number
  step?: number
}
export interface NodeConfig {
  read_only?: string[]
  fields: ConfigField[]
  values: Record<string, string | number | boolean>
}
const path = (id: string) => `/api/cc/api/v0/nodes/${encodeURIComponent(id)}/node-config`
export async function getNodeConfig(id: string, schema = false): Promise<NodeConfig> {
  return (await apiClient.get<NodeConfig>(path(id) + (schema ? '?schema=1' : ''))).data
}
export async function saveNodeConfig(id: string, settings: NodeConfig['values']): Promise<NodeConfig> {
  return (await apiClient.patch<NodeConfig>(path(id), { settings })).data
}
export async function sendBasicNodeConfig(id: string, settings: NodeConfig['values']): Promise<void> {
  await apiClient.post(`${path(id)}/basic`, { settings })
}
