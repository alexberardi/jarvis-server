export type GpuType = 'nvidia' | 'amd' | 'amd-rocm' | 'apple' | 'none'

/** Explicit Whisper GPU backend (default "cpu"); independent of hardware.gpuType. */
export type WhisperBackend = 'cpu' | 'cuda' | 'vulkan' | 'rocm'

export interface HardwareInfo {
  platform: 'darwin' | 'linux' | 'win32'
  arch: string
  totalMemoryGb: number
  gpuName: string | null
  gpuVramMb: number | null
  gpuType: GpuType
  recommendedBackends: string[]
  recommendedBackend: string
}

export interface WizardState {
  currentStep: number
  totalSteps: number
  enabledModules: string[]
  portOverrides: Record<string, number>
  infraPortOverrides: Record<string, number>
  secrets: Record<string, string>
  dbUser: string
  whisperModel: string
  whisperModelPath: string
  whisperBackend?: WhisperBackend
  llmInterface: string
  deploymentMode: 'local' | 'remote-llm'
  deploymentTarget: 'standard' | 'compose-export'
  remoteLlmUrl: string
  remoteWhisperUrl: string
  platform: 'darwin' | 'linux' | 'win32'
  hardware: HardwareInfo | null
  relayEnabled: boolean
  installRunning: boolean
  installComplete: boolean
  /**
   * Service IDs the user has opted to run natively (macOS only). Excluded from
   * the generated docker-compose so they can use Metal / MLX / MPS via launchd.
   */
  nativeServices: string[]
}

export type WizardAction =
  | { type: 'SET_STEP'; step: number }
  | { type: 'NEXT_STEP' }
  | { type: 'PREV_STEP' }
  | { type: 'SET_ENABLED_MODULES'; modules: string[] }
  | { type: 'TOGGLE_MODULE'; serviceId: string; enabled: boolean }
  | { type: 'SET_PORT_OVERRIDE'; serviceId: string; port: number }
  | { type: 'SET_INFRA_PORT_OVERRIDE'; infraId: string; port: number }
  | { type: 'SET_SECRET'; name: string; value: string }
  | { type: 'SET_SECRETS'; secrets: Record<string, string> }
  | { type: 'SET_DB_USER'; user: string }
  | { type: 'SET_WHISPER_MODEL'; model: string }
  | { type: 'SET_WHISPER_BACKEND'; backend: WhisperBackend }
  | { type: 'SET_LLM_INTERFACE'; interfaceId: string }
  | { type: 'SET_DEPLOYMENT_MODE'; mode: 'local' | 'remote-llm' }
  | { type: 'SET_DEPLOYMENT_TARGET'; target: 'standard' | 'compose-export' }
  | { type: 'SET_REMOTE_LLM_URL'; url: string }
  | { type: 'SET_REMOTE_WHISPER_URL'; url: string }
  | { type: 'SET_PLATFORM'; platform: 'darwin' | 'linux' | 'win32' }
  | { type: 'SET_HARDWARE'; hardware: HardwareInfo }
  | { type: 'SET_RELAY_ENABLED'; enabled: boolean }
  | { type: 'SET_INSTALL_RUNNING'; running: boolean }
  | { type: 'SET_INSTALL_COMPLETE' }
  | { type: 'SET_NATIVE_SERVICES'; services: string[] }
  | { type: 'TOGGLE_NATIVE_SERVICE'; serviceId: string; enabled: boolean }

export interface InstallStatus {
  configured: boolean
  composePath?: string
  reason?: string
  state?: 'fresh' | 'generated' | 'partial' | 'running' | 'complete' | 'deployed-needs-account'
  running?: string[]
  stopped?: string[]
  deployMode?: 'standard' | 'compose-export'
}

export interface PreflightCheck {
  name: string
  status: 'pass' | 'warn' | 'fail'
  message: string
  details?: string
}

export interface PreflightResult {
  checks: PreflightCheck[]
  canProceed: boolean
}

export interface ServiceHealthResult {
  healthy: boolean
  error?: string
}

export interface HealthStatus {
  [serviceId: string]: {
    healthy: boolean
    url: string
    error?: string
  }
}

export interface GenerateResult {
  ok: boolean
  composePath: string
  files: string[]
  serviceCount: number
}

export interface RegisterResult {
  registered: string[]
  failed: Array<{ serviceId: string; error: string }>
  needsRestart: boolean
}

export interface ServiceDefinition {
  id: string
  name: string
  description: string
  category: 'core' | 'recommended' | 'optional'
  port: number
  image: string
  healthCheck: string
  dependsOn: string[]
  /** true = recommended to run natively on macOS for Metal/MLX/MPS access. */
  nativeCapable?: boolean
  database?: string
  modelOptions?: Array<{
    id: string
    name: string
    size: string
    default?: boolean
    builtin?: boolean
  }>
  llmInterfaceOptions?: Array<{
    id: string
    name: string
    description: string
    default?: boolean
  }>
}

export interface ServiceRegistry {
  version: string
  services: ServiceDefinition[]
  infrastructure: Array<{
    id: string
    name: string
    description: string
    port: number
  }>
}
