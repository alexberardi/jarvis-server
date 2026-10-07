/**
 * Connections (AD7, A4 "As built" #8): jarvisd's own listeners (read-only), external registry
 * entries (add/remove), and app clients for external callers (create/rotate/revoke). App keys
 * come back exactly once, from create and rotate; nothing else ever returns them.
 */
import { apiClient } from './client'

export interface HealthStatus {
  healthy: boolean
  latency_ms?: number
  error?: string
}

export interface ListenerConnection {
  name: string
  url: string
  port: number
  /** "listener" | "broker". */
  managed: string
  /** null when fetched with health=false. */
  health: HealthStatus | null
}

export interface ExternalConnection {
  name: string
  url: string
  health_path: string
  description: string
  /** "setting" when synced from a jarvisd setting (e.g. the Pantry), "" when added by hand. */
  managed: string
  removable: boolean
  health: HealthStatus | null
}

export interface AppClient {
  app_id: string
  name: string
  is_active: boolean
  created_at: string
  last_rotated_at: string | null
}

export interface ConnectionsResponse {
  listeners: ListenerConnection[]
  external: ExternalConnection[]
  apps: AppClient[]
}

/** A key response: show it once, then forget it. */
export interface AppKey {
  app_id: string
  app_key: string
  name?: string
  is_active?: boolean
  last_rotated_at?: string | null
}

/** getConnections lists everything; `health` probes every row (slower) or skips it. */
export async function getConnections(health: boolean): Promise<ConnectionsResponse> {
  const { data } = await apiClient.get<ConnectionsResponse>('/api/connections', {
    params: health ? undefined : { health: 'false' },
  })
  return data
}

export interface NewService {
  name: string
  url: string
  health_path?: string
  description?: string
}

export async function addService(s: NewService): Promise<ExternalConnection> {
  const { data } = await apiClient.post<ExternalConnection>('/api/connections/services', s)
  return data
}

export async function removeService(name: string): Promise<void> {
  await apiClient.delete(`/api/connections/services/${encodeURIComponent(name)}`)
}

export async function createApp(appId: string, name: string): Promise<AppKey> {
  const { data } = await apiClient.post<AppKey>('/api/connections/apps', { app_id: appId, name })
  return data
}

/** rotateApp issues a new key (and reactivates a revoked client). */
export async function rotateApp(appId: string): Promise<AppKey> {
  const { data } = await apiClient.post<AppKey>(`/api/connections/apps/${encodeURIComponent(appId)}/rotate`)
  return data
}

export async function revokeApp(appId: string): Promise<void> {
  await apiClient.post(`/api/connections/apps/${encodeURIComponent(appId)}/revoke`)
}
