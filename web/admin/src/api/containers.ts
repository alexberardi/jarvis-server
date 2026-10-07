import { apiClient } from './client'
import type { ContainersResponse, ContainerDetailResponse } from '@/types/containers'

export async function getContainers(): Promise<ContainersResponse> {
  const { data } = await apiClient.get<ContainersResponse>('/api/containers')
  return data
}

export async function getContainer(id: string): Promise<ContainerDetailResponse> {
  const { data } = await apiClient.get<ContainerDetailResponse>(`/api/containers/${id}`)
  return data
}

export async function restartContainer(id: string): Promise<{ success: boolean; message: string }> {
  const { data } = await apiClient.post<{ success: boolean; message: string }>(
    `/api/containers/${id}/restart`,
  )
  return data
}
