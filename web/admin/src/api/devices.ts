import { apiClient } from './client'
import type { ChatAction } from './chat'

const CC = '/api/cc/api/v0'
const household = (id: string) => `${CC}/households/${encodeURIComponent(id)}`
const devicePath = (hh: string, id: string) => `${household(hh)}/devices/${encodeURIComponent(id)}`

export interface DeviceIdentity {
  entity_id: string
  name: string
  domain: string
  protocol: string | null
  manufacturer?: string | null
  model?: string | null
  local_ip?: string | null
  mac_address?: string | null
  cloud_id?: string | null
  device_class?: string | null
}

export interface Device extends DeviceIdentity {
  id: string
  household_id: string
  node_id?: string | null
  room_id: string | null
  room_name: string | null
  source: string
  is_controllable: boolean
  supported_actions: ChatAction[] | null
}

export interface DiscoveredDevice extends DeviceIdentity {
  already_registered: boolean
  existing_device_id: string | null
  source?: string
  area?: string | null
  state?: string | null
  room_name?: string | null
}

export interface DeviceImport extends DeviceIdentity { source: 'direct'; room_id: string | null }
export interface DeviceRoom { id: string; name: string }
export interface DeviceNode { node_id: string; name: string | null; room: string | null; online: boolean; node_kind?: string | null }
export interface DeviceConfig { primary_node_id: string; use_external_devices: boolean; nodes: DeviceNode[] }
export interface DeviceInput { type: string; prompt: string; session_id: string; follow_up_action: string }
export interface DeviceResult { success: boolean; error: string | null; input_required?: DeviceInput | null }
export interface DeviceState { state: Record<string, unknown> | null; ui_hints: { control_type?: string } | null; error: string | null }
export interface DeviceJob { status: 'pending' | 'completed' | 'failed' | 'expired'; devices: DiscoveredDevice[] | null; error_message: string | null; can_edit_devices?: boolean | null; manager_name?: string | null }
export type DeviceJobKind = 'device-scan' | 'device-list'

export const deviceNodeName = (n: DeviceNode) => n.name || n.room || n.node_id
export const defaultDeviceNode = (config: DeviceConfig) =>
  config.nodes.find((n) => n.node_id === config.primary_node_id && n.online) ?? config.nodes.find((n) => n.online)

export async function getDeviceConfig(hh: string, signal?: AbortSignal): Promise<DeviceConfig> {
  return (await apiClient.get(`${household(hh)}/smart-home/config`, { signal })).data
}
export async function listDevices(hh: string, signal?: AbortSignal): Promise<Device[]> {
  return (await apiClient.get(`${household(hh)}/devices`, { signal })).data
}
export async function listDeviceRooms(hh: string, signal?: AbortSignal): Promise<DeviceRoom[]> {
  return (await apiClient.get(`${household(hh)}/rooms`, { signal })).data
}
export async function importDevices(hh: string, node: string, devices: DeviceImport[], signal: AbortSignal): Promise<{ created: number; updated: number }> {
  return (await apiClient.post(`${household(hh)}/devices/import`, { node_id: node, devices }, { signal, timeout: 30000 })).data
}
export async function updateDevice(hh: string, id: string, data: { name: string; room_id: string | null }, signal: AbortSignal): Promise<Device> {
  return (await apiClient.patch(devicePath(hh, id), data, { signal })).data
}
export async function removeDevice(hh: string, id: string, signal: AbortSignal): Promise<void> {
  await apiClient.delete(devicePath(hh, id), { signal })
}
export async function getDeviceState(hh: string, id: string, signal?: AbortSignal): Promise<DeviceState> {
  return (await apiClient.get(`${devicePath(hh, id)}/state`, { signal, timeout: 15000 })).data
}
export async function controlDevice(hh: string, id: string, action: string, data: Record<string, unknown> | undefined, signal: AbortSignal): Promise<DeviceResult> {
  return (await apiClient.post(`${devicePath(hh, id)}/control`, { action, data }, { signal, timeout: 30000 })).data
}

function delay(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    signal.throwIfAborted()
    const abort = () => { clearTimeout(timer); reject(signal.reason) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, 2000)
    signal.addEventListener('abort', abort, { once: true })
  })
}

/** A bounded node job. Closing/switching views cancels HTTP and polling, not the node scan. */
export async function requestDeviceJob(node: string, kind: DeviceJobKind, signal: AbortSignal): Promise<DeviceJob> {
  signal.throwIfAborted()
  const controller = new AbortController()
  const abort = () => controller.abort(signal.reason)
  signal.addEventListener('abort', abort, { once: true })
  const timeout = setTimeout(() => controller.abort(new Error('Device request expired. Check the node and try again.')), 120000)
  const path = `${CC}/nodes/${encodeURIComponent(node)}/${kind}`
  const options = { signal: controller.signal, timeout: 15000 }
  try {
    const { data: request } = await apiClient.post<{ id: string }>(`${path}/request`, undefined, options)
    for (;;) {
      controller.signal.throwIfAborted()
      let result: DeviceJob | undefined
      try {
        result = (await apiClient.get<DeviceJob>(`${path}/${encodeURIComponent(request.id)}`, options)).data
      } catch (error) {
        controller.signal.throwIfAborted()
        const status = (error as { response?: { status: number } }).response?.status
        if (status && status < 500) throw error
      }
      controller.signal.throwIfAborted()
      if (result?.status === 'completed') return result
      if (result?.status === 'failed') throw new Error(result.error_message || 'The node could not complete the request.')
      if (result?.status === 'expired') throw new Error('Device request expired. Try again.')
      await delay(controller.signal)
    }
  } catch (error) {
    if (controller.signal.aborted) throw controller.signal.reason
    throw error
  } finally {
    clearTimeout(timeout)
    signal.removeEventListener('abort', abort)
  }
}
