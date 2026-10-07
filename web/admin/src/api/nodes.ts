import { apiClient } from './client'
import type { Household, HouseholdNode, TrainAdapterResponse } from '@/types/nodes'

export async function getHouseholds(): Promise<Household[]> {
  const { data } = await apiClient.get<Household[]>('/api/nodes')
  return data
}

export async function getHouseholdNodes(householdId: string): Promise<HouseholdNode[]> {
  const { data } = await apiClient.get<HouseholdNode[]>(`/api/nodes/${householdId}/nodes`)
  return data
}

export async function trainNodeAdapter(nodeId: string): Promise<TrainAdapterResponse> {
  const { data } = await apiClient.post<TrainAdapterResponse>(`/api/nodes/${nodeId}/train-adapter`)
  return data
}
