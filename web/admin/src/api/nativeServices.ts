import { apiClient } from './client'

export interface NativeServiceStatus {
  serviceId: string
  label: string
  installed: boolean
  running: boolean
  pid?: number
  sourceDir?: string
  port?: number
  logs: { stdout: string; stderr: string }
}

export interface NativeServiceEntry {
  id: string
  name: string
  description: string
  port: number
  status: NativeServiceStatus
}

export interface NativeServicesResponse {
  supported: boolean
  services: NativeServiceEntry[]
}

export interface NativeLogResponse {
  file: string
  content: string
  size: number
}

export async function getNativeServices(): Promise<NativeServicesResponse> {
  const { data } = await apiClient.get<NativeServicesResponse>('/api/native-services')
  return data
}

export async function startNativeService(id: string): Promise<{ ok: true; label: string }> {
  const { data } = await apiClient.post<{ ok: true; label: string }>(
    `/api/native-services/${id}/restart`,
  )
  return data
}

export async function stopNativeService(id: string): Promise<{ ok: true; label: string }> {
  const { data } = await apiClient.post<{ ok: true; label: string }>(
    `/api/native-services/${id}/stop`,
  )
  return data
}

export async function uninstallNativeService(
  id: string,
): Promise<{ ok: true; label: string; removedPlist: string }> {
  const { data } = await apiClient.post<{ ok: true; label: string; removedPlist: string }>(
    `/api/native-services/${id}/uninstall`,
  )
  return data
}

export async function getNativeLogs(
  id: string,
  stream: 'stdout' | 'stderr' = 'stderr',
  lines = 200,
): Promise<NativeLogResponse> {
  const { data } = await apiClient.get<NativeLogResponse>(
    `/api/native-services/${id}/logs`,
    { params: { stream, lines } },
  )
  return data
}
