import { apiClient } from './client'

export type DoctorStatus = 'ok' | 'warn' | 'fail'

/** One `jarvisd doctor` check (internal/doctor). `fix` is a command or instruction, when known. */
export interface DoctorCheck {
  name: string
  status: DoctorStatus | string
  detail: string
  fix?: string
}

export interface DoctorReport {
  status: DoctorStatus | string
  checks: DoctorCheck[]
  ran_at: string
}

/**
 * getDoctor is GET /api/doctor: open while no superuser exists (the wizard's Check step), gated
 * after. Results are cached server-side for 30 s; `refresh` runs the checks again.
 */
export async function getDoctor(refresh = false): Promise<DoctorReport> {
  const { data } = await apiClient.get<DoctorReport>('/api/doctor', {
    params: refresh ? { refresh: 'true' } : undefined,
  })
  return data
}
