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
  /**
   * A household may set its own value (in the mobile app); `value` is then the default for
   * every household without one. Absent from older backends.
   */
  household_scoped?: boolean
  /** Households with their own value, by name. Present when household_scoped. */
  household_values?: HouseholdValue[]
  /** How many households have no value of their own. Present when household_scoped. */
  households_using_default?: number
}

export interface HouseholdValue {
  household_id: string
  household_name: string
  value: unknown
  updated_at?: string
}

export interface ServiceSettingsResult {
  service_name: string
  /** Human label for the module (jarvisd); absent from older backends. */
  display_name?: string
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
  /** Set when the write was one household's own value. */
  household_id?: string
}
