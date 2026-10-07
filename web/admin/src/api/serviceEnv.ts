import { apiClient } from './client'

export interface ServiceEnvVar {
  name: string
  description: string
  required: boolean
  secret: boolean
  user_supplied: boolean
  is_set: boolean
  /** Current .env value — only present for non-secret user-supplied vars. */
  value: string | null
  /** Registry default — only present for read-only generated vars. */
  default: string | null
}

export interface ServiceEnvEntry {
  service_id: string
  service_name: string
  env_file_exists: boolean
  container_id: string | null
  container_running: boolean
  vars: ServiceEnvVar[]
}

export interface ServiceEnvResponse {
  services: ServiceEnvEntry[]
}

export interface ServiceEnvUpdateResponse {
  success: boolean
  updated: string[]
  restart_required: boolean
  container_id: string | null
}

export async function getServiceEnv(): Promise<ServiceEnvResponse> {
  const { data } = await apiClient.get<ServiceEnvResponse>('/api/service-env')
  return data
}

export async function updateServiceEnv(
  serviceId: string,
  values: Record<string, string>,
): Promise<ServiceEnvUpdateResponse> {
  const { data } = await apiClient.put<ServiceEnvUpdateResponse>(
    `/api/service-env/${serviceId}`,
    { values },
  )
  return data
}

export interface ServiceEnvApplyResponse {
  success: boolean
  /** 'recreated' — stack compose recreated the container (env reloaded).
   *  'manual' — this container isn't stack-managed; message + command tell
   *  the operator how to apply (a plain restart never re-reads env_file). */
  mode: 'recreated' | 'manual'
  message?: string
  command?: string
}

export async function applyServiceEnv(serviceId: string): Promise<ServiceEnvApplyResponse> {
  const { data } = await apiClient.post<ServiceEnvApplyResponse>(
    `/api/service-env/${serviceId}/apply`,
  )
  return data
}
