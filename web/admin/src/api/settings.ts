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

function settingPath(serviceName: string, key: string, householdId?: string): string {
  const path = `/api/settings/${encodeURIComponent(serviceName)}/${key}`
  return householdId ? `${path}?household_id=${encodeURIComponent(householdId)}` : path
}

/** Sets the default (no householdId) or one household's own value. */
export async function updateSetting(
  serviceName: string,
  key: string,
  value: unknown,
  householdId?: string,
): Promise<ServiceUpdateResponse> {
  const { data } = await apiClient.put<ServiceUpdateResponse>(settingPath(serviceName, key, householdId), {
    value,
  })
  return data
}

/** Removes a household's own value, so it uses the default again. */
export async function resetHouseholdSetting(
  serviceName: string,
  key: string,
  householdId: string,
): Promise<ServiceUpdateResponse> {
  const { data } = await apiClient.delete<ServiceUpdateResponse>(settingPath(serviceName, key, householdId))
  return data
}
