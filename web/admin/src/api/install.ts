import { apiClient } from './client'
import type {
  InstallStatus,
  HardwareInfo,
  WizardState,
  GenerateResult,
  RegisterResult,
  HealthStatus,
  ServiceRegistry,
  PreflightResult,
} from '@/types/wizard'

export async function getInstallStatus(): Promise<InstallStatus> {
  const { data } = await apiClient.get<InstallStatus>('/api/install/status')
  return data
}

export async function runPreflight(enabledServices?: string[]): Promise<PreflightResult> {
  const params = enabledServices?.length ? { services: enabledServices.join(',') } : {}
  const { data } = await apiClient.get<PreflightResult>('/api/install/preflight', { params })
  return data
}

export async function getHardwareInfo(): Promise<HardwareInfo> {
  const { data } = await apiClient.get<HardwareInfo>('/api/install/hardware')
  return data
}

export async function generateInstall(state: WizardState): Promise<GenerateResult> {
  const { data } = await apiClient.post<GenerateResult>('/api/install/generate', state)
  return data
}

export async function registerServices(
  portOverrides?: Record<string, number>,
): Promise<RegisterResult> {
  const { data } = await apiClient.post<RegisterResult>('/api/install/register', {
    portOverrides,
  })
  return data
}

export async function getInstallHealth(): Promise<HealthStatus> {
  const { data } = await apiClient.get<HealthStatus>('/api/install/health')
  return data
}

export async function createAccount(
  email: string,
  password: string,
  displayName: string,
): Promise<{ ok: boolean; email: string }> {
  const { data } = await apiClient.post('/api/install/account', {
    email,
    password,
    displayName,
  })
  return data
}

export async function getServiceRegistry(): Promise<ServiceRegistry> {
  const { data } = await apiClient.get<ServiceRegistry>('/api/install/registry')
  return data
}

export async function getInstallDefaults(): Promise<{ enabledModules: string[] }> {
  const { data } = await apiClient.get<{ enabledModules: string[] }>('/api/install/defaults')
  return data
}
