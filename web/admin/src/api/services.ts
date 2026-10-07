import { apiClient } from './client'
import type {
  ServiceRegistryResponse,
  ServiceRegisterRequest,
  ServiceRegisterResponse,
  KeyRotateRequest,
  KeyRotateResponse,
  HealthProbeRequest,
  HealthProbeResponse,
  AddServiceRequest,
  ServiceSuggestionsResponse,
} from '@/types/services'

export async function getServiceRegistry(): Promise<ServiceRegistryResponse> {
  const { data } = await apiClient.get<ServiceRegistryResponse>('/api/services/registry')
  return data
}

export async function registerServices(
  request: ServiceRegisterRequest,
): Promise<ServiceRegisterResponse> {
  const { data } = await apiClient.post<ServiceRegisterResponse>(
    '/api/services/register',
    request,
  )
  return data
}

export async function rotateServiceKey(
  request: KeyRotateRequest,
): Promise<KeyRotateResponse> {
  const { data } = await apiClient.post<KeyRotateResponse>(
    '/api/services/rotate-key',
    request,
  )
  return data
}

export async function probeServiceHealth(
  request: HealthProbeRequest,
): Promise<HealthProbeResponse> {
  const { data } = await apiClient.post<HealthProbeResponse>(
    '/api/services/probe',
    request,
  )
  return data
}

export async function addService(
  request: AddServiceRequest,
): Promise<ServiceRegisterResponse> {
  const { data } = await apiClient.post<ServiceRegisterResponse>(
    '/api/services/register',
    {
      services: [
        {
          name: request.name,
          host: request.host,
          port: request.port,
          scheme: request.scheme,
          health_path: request.health_path,
          description: request.description,
        },
      ],
    },
  )
  return data
}

export async function deleteService(name: string): Promise<void> {
  await apiClient.delete(`/api/services/${encodeURIComponent(name)}`)
}

export async function getServiceSuggestions(): Promise<ServiceSuggestionsResponse> {
  const { data } = await apiClient.get<ServiceSuggestionsResponse>('/api/services/suggestions')
  return data
}
