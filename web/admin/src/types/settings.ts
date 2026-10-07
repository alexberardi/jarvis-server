export interface SettingResponse {
  key: string
  value: unknown
  value_type: 'string' | 'int' | 'float' | 'bool' | 'json'
  category: string
  description: string | null
  requires_reload: boolean
  is_secret: boolean
  env_fallback: string | null
  from_db: boolean
  options: string[] | null
}

export interface ServiceSettingsResult {
  service_name: string
  success: boolean
  settings: SettingResponse[]
  error: string | null
  latency_ms: number | null
}

export interface AggregatedSettingsResponse {
  services: ServiceSettingsResult[]
  total_services: number
  successful_services: number
  failed_services: number
}

export interface ServiceUpdateResponse {
  service_name: string
  success: boolean
  key: string
  requires_reload: boolean
  message: string | null
  error: string | null
}
