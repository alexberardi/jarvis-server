import { apiClient } from './client'
import type { AggregatedSettingsResponse, ServiceUpdateResponse } from '@/types/settings'

export async function getAllSettings(): Promise<AggregatedSettingsResponse> {
  const { data } = await apiClient.get<AggregatedSettingsResponse>('/api/settings/')
  return data
}

export async function getServiceSettings(serviceName: string): Promise<AggregatedSettingsResponse> {
  const { data } = await apiClient.get<AggregatedSettingsResponse>(
    `/api/settings/?service=${encodeURIComponent(serviceName)}`,
  )
  return data
}

export async function updateSetting(
  serviceName: string,
  key: string,
  value: unknown,
): Promise<ServiceUpdateResponse> {
  const { data } = await apiClient.put<ServiceUpdateResponse>(
    `/api/settings/${encodeURIComponent(serviceName)}/${key}`,
    { value },
  )
  return data
}
