import { apiClient } from './client'

export interface ModelInfo {
  name: string
  size: number
  sizeFormatted: string
}

export interface DownloadRequest {
  repo: string
  filename?: string
  token?: string
}

export async function getInstalledModels(): Promise<ModelInfo[]> {
  const { data } = await apiClient.get<{ models: ModelInfo[] }>('/api/models/installed')
  return data.models
}

export async function downloadModel(req: DownloadRequest): Promise<{ success: boolean; output: string; message: string }> {
  const { data } = await apiClient.post('/api/models/download', req)
  return data
}

export async function deleteModel(name: string): Promise<void> {
  await apiClient.delete(`/api/models/${encodeURIComponent(name)}`)
}

export async function enableWhisperAutodownload(
  enabled: boolean,
): Promise<{ success: boolean; enabled: boolean; restarted: boolean }> {
  const { data } = await apiClient.post('/api/models/whisper-autodownload', { enabled })
  return data
}
