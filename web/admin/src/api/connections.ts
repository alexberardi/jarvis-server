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
  /** What clients that reach jarvisd through a public hostname (a tunnel) are given; null when unset. */
  public_url: string | null
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
  public_url: string | null
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
  public_url?: string
}

export async function addService(s: NewService): Promise<ExternalConnection> {
  const { data } = await apiClient.post<ExternalConnection>('/api/connections/services', s)
  return data
}

export async function removeService(name: string): Promise<void> {
  await apiClient.delete(`/api/connections/services/${encodeURIComponent(name)}`)
}

/** setPublicURL sets a row's public base URL; null (or '') clears it. */
export async function setPublicURL(name: string, publicURL: string | null): Promise<{ name: string; url: string; public_url: string | null }> {
  const { data } = await apiClient.put<{ name: string; url: string; public_url: string | null }>(
    `/api/connections/services/${encodeURIComponent(name)}/public_url`,
    { public_url: publicURL || null },
  )
  return data
}

const PUBLIC_SCHEMES = ['http', 'https', 'ws', 'wss', 'mqtt', 'mqtts']

/**
 * publicURLError mirrors the server's check (config/public.go) so the form can say what is
 * wrong before saving: scheme http/https/ws/wss/mqtt/mqtts, a host, an optional port, no path.
 * Returns null when valid (or empty, which clears).
 */
export function publicURLError(raw: string): string | null {
  const v = raw.trim()
  if (!v) return null
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)(\/?)$/.exec(v)
  if (!m) {
    return /^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(v)
      ? 'Base URL only: scheme, host and optional port, no path'
      : 'Start with a scheme, e.g. https://'
  }
  const [, scheme, authority] = m
  if (!PUBLIC_SCHEMES.includes(scheme.toLowerCase())) return 'Use http, https, ws, wss, mqtt or mqtts'
  if (authority.includes('@')) return 'No user name or password in the URL'
  const hp = /^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9._-]+)(?::(\d{1,5}))?$/.exec(authority)
  if (!hp) return 'Enter a host name, with an optional :port'
  const port = hp[2]
  if (port !== undefined && (Number(port) < 1 || Number(port) > 65535)) return 'Port must be 1–65535'
  const host = hp[1].toLowerCase()
  if (host === 'localhost' || host.startsWith('127.') || host === '[::1]') return "A public URL can't be this machine's loopback address"
  return null
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
