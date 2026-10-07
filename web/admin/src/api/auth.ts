import { apiClient } from './client'
import { SETUP_TOKEN_HEADER } from '@/auth/setupToken'

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

/** setupState is the always-open GET /api/setup/state (reduced view without a superuser token). */
export interface SetupState {
  needs_superuser: boolean
  setup_token_required: boolean
  version: string
  superuser: boolean
  setup_token_file?: string
}

export async function getSetupState(): Promise<SetupState> {
  const { data } = await apiClient.get<SetupState>('/api/setup/state')
  return data
}
