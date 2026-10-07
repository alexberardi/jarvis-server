import { apiClient } from './client'
import type { SystemInfo } from '@/types/system'

export async function getSystemInfo(): Promise<SystemInfo> {
  const { data } = await apiClient.get<SystemInfo>('/api/system/info')
  return data
}
