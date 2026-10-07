import { apiClient } from './client'

export interface QuickSetPreset {
  id: string
  name: string
  family: string
  description: string
  chatFormat: string
  promptProvider: string
  defaultBackend: string
  defaultContextWindow: number
  isCustom?: boolean
}

export interface QuickSetsResponse {
  presets: QuickSetPreset[]
  currentValues: {
    modelName: string
    contextWindow: number
  }
}

export interface ApplyQuickSetRequest {
  presetId: string
  modelName: string
  contextWindow?: number
  backend?: string
  chatFormat?: string
  promptProvider?: string
  targets?: ('live' | 'background')[]
}

export interface ApplyQuickSetResponse {
  success: boolean
  applied: { key: string; service: string; success: boolean; error?: string }[]
  message: string
}

export interface CreateCustomPresetRequest {
  name: string
  family: string
  description?: string
  chatFormat: string
  promptProvider: string
  defaultBackend?: string
  defaultContextWindow?: number
}

export async function getQuickSets(): Promise<QuickSetsResponse> {
  const { data } = await apiClient.get<QuickSetsResponse>('/api/quick-sets')
  return data
}

export async function applyQuickSet(req: ApplyQuickSetRequest): Promise<ApplyQuickSetResponse> {
  const { data } = await apiClient.post<ApplyQuickSetResponse>('/api/quick-sets/apply', req)
  return data
}

export async function createCustomPreset(
  req: CreateCustomPresetRequest,
): Promise<{ preset: QuickSetPreset }> {
  const { data } = await apiClient.post<{ preset: QuickSetPreset }>('/api/quick-sets/custom', req)
  return data
}

export async function deleteCustomPreset(id: string): Promise<{ success: boolean }> {
  const { data } = await apiClient.delete<{ success: boolean }>(`/api/quick-sets/custom/${id}`)
  return data
}
