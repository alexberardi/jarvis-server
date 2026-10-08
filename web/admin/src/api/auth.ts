import { apiClient } from './client'
import { SETUP_TOKEN_HEADER } from '@/auth/setupToken'
import type { Hardware, Label, Placement, PromptProvider } from './llm'

export interface AuthUser {
  id: number
  email: string
  username?: string
  is_superuser: boolean
  must_change_password?: boolean
}

export interface TokenResponse {
  access_token: string
  refresh_token: string
  token_type: 'bearer'
  user: AuthUser
  must_change_password?: boolean
}

export async function getSetupStatus(): Promise<{ needs_setup: boolean }> {
  const { data } = await apiClient.get<{ needs_setup: boolean }>('/api/auth/setup-status')
  return data
}

/** setup creates the first superuser. jarvisd requires the setup token while none exists (AD2). */
export async function setup(
  email: string,
  password: string,
  username?: string,
  setupToken?: string | null,
): Promise<TokenResponse> {
  const headers: Record<string, string> = {}
  if (setupToken) headers[SETUP_TOKEN_HEADER] = setupToken
  const { data } = await apiClient.post<TokenResponse>(
    '/api/auth/setup',
    { email, password, username },
    { headers },
  )
  return data
}

export async function login(email: string, password: string): Promise<TokenResponse> {
  const { data } = await apiClient.post<TokenResponse>('/api/auth/login', { email, password })
  return data
}

export async function refresh(refreshToken: string): Promise<TokenResponse> {
  const { data } = await apiClient.post<TokenResponse>('/api/auth/refresh', {
    refresh_token: refreshToken,
  })
  return data
}

/** logout revokes the refresh token's family server-side (always 204). */
export async function logout(refreshToken: string): Promise<void> {
  await apiClient.post('/api/auth/logout', { refresh_token: refreshToken })
}

/** changePassword revokes every session and returns a fresh pair the caller must adopt. */
export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<TokenResponse> {
  const { data } = await apiClient.post<TokenResponse>('/api/auth/change-password', {
    current_password: currentPassword,
    new_password: newPassword,
  })
  return data
}

/** The hardware summary the setup wizard's Hardware step starts from (llm `SetupHardware`, AD3). */
export interface SetupHardware {
  hardware: Hardware
  proposal: Record<string, Placement>
  /** Per engine kind ("llama-server", "whisper-server"), the flavours this platform has builds for. */
  flavours: Record<string, string[]>
}

export interface SetupDoctorSummary {
  status: 'ok' | 'warn' | 'fail' | string
  failing: string[]
  ran_at: string
}

/** A model job's state in the setup checklist (AD3b; Go admin.JobSummary). */
export type JobState = 'ready' | 'loading' | 'downloading' | 'failed' | 'missing'

export interface SetupJob {
  /** llm, stt, voice, speaker, memory: also the wizard's step names. */
  job: string
  /** The label that decides the state; labels adds any installed with it (live + background). */
  label: Label
  labels: Label[]
  required: boolean
  state: JobState | string
  label_state: string
  /** The install it waits on (downloading) or whose failure it reports (failed). */
  install?: {
    id: number
    model_id: string
    state: string
    phase: string
    bytes_done: number
    bytes_total: number
    error?: string
  }
}

/**
 * setupState is the always-open GET /api/setup/state. Anonymous callers (or a non-superuser)
 * get the reduced view; a superuser token adds the rest (A3 "As built" #6).
 */
export interface SetupState {
  needs_superuser: boolean
  setup_token_required: boolean
  version: string
  superuser: boolean
  setup_token_file?: string
  // Superuser view only:
  labels?: Partial<Record<Label, string>>
  /** live is ready, degraded or remote. */
  live_ready?: boolean
  /** live has a model assigned, even while it is still loading. */
  models_configured?: boolean
  /** null without the local engine stack. */
  hardware?: SetupHardware | null
  hardware_url?: string
  prompt_provider?: PromptProvider | null
  doctor?: SetupDoctorSummary
  households?: number
  nodes?: number
  /** The wizard reached Done (admin setting setup.completed). */
  setup_completed?: boolean
  /** Where the wizard resumes in any tab or browser; "" once it was finished (A10 F9, AD3b). */
  setup_step?: string
  /** The per-job model checklist, in wizard order (AD3b). */
  jobs?: SetupJob[]
}

export async function getSetupState(): Promise<SetupState> {
  const { data } = await apiClient.get<SetupState>('/api/setup/state')
  return data
}
