import { apiClient } from './client'

export interface LlmStatus {
  configured: boolean
  model?: string
  backend?: string
}

export interface ConfigureResponse {
  success: boolean
  message: string
  settingsResult?: unknown
}

export interface DownloadResponse {
  success: boolean
  output: string
  message: string
}

export async function getLlmStatus(): Promise<LlmStatus> {
  const { data } = await apiClient.get<LlmStatus>('/api/llm-setup/status')
  return data
}

export async function configureLlm(settings: Record<string, unknown>): Promise<ConfigureResponse> {
  const { data } = await apiClient.post<ConfigureResponse>('/api/llm-setup/configure', { settings })
  return data
}

export async function downloadModel(
  repo: string,
  filename?: string,
  token?: string,
): Promise<DownloadResponse> {
  const { data } = await apiClient.post<DownloadResponse>('/api/llm-setup/download', {
    repo,
    filename,
    token,
  })
  return data
}
