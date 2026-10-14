import axios from 'axios'

export const apiClient = axios.create({
  baseURL: '',
})

export function setAuthToken(token: string | null): void {
  if (token) {
    apiClient.defaults.headers.common['Authorization'] = `Bearer ${token}`
  } else {
    delete apiClient.defaults.headers.common['Authorization']
  }
}

let refreshFn: (() => Promise<string | null>) | null = null

export function setRefreshFunction(fn: () => Promise<string | null>): void {
  refreshFn = fn
}

/**
 * accessToken is the bearer token for calls made outside axios (the fetch() stream readers,
 * which `EventSource` can't do because it can't send a header).
 */
export function accessToken(): string | null {
  const h = apiClient.defaults.headers.common['Authorization']
  if (typeof h === 'string' && h.startsWith('Bearer ')) return h.slice(7)
  return localStorage.getItem('jarvis-admin:access_token')
}

/** refreshAccessToken swaps the refresh token for a new access token (null when that fails). */
export async function refreshAccessToken(): Promise<string | null> {
  return refreshFn ? refreshFn() : null
}

let logoutFn: (() => void) | null = null

export function setLogoutFunction(fn: () => void): void {
  logoutFn = fn
}

let mustChangeFn: (() => void) | null = null

/** setMustChangePasswordFunction registers what to do when the server says the password must change. */
export function setMustChangePasswordFunction(fn: () => void): void {
  mustChangeFn = fn
}

/** notifyPasswordChangeRequired flips the session to the change-password screen (fetch callers). */
export function notifyPasswordChangeRequired(): void {
  mustChangeFn?.()
}

/**
 * isPasswordChangeRequired is jarvisd's answer to a token whose account must change its
 * password first (a temp-password reset, G8): 403 with code password_change_required. Only
 * GET /api/auth/me and POST /api/auth/change-password accept such a token.
 */
export function isPasswordChangeRequired(error: unknown): boolean {
  const res = (error as { response?: { status?: number; data?: { code?: unknown } } })?.response
  return res?.status === 403 && res.data?.code === 'password_change_required'
}

// Ensure Authorization header is set from localStorage (survives page refresh).
// AuthContext is the only writer, under the namespaced key (O5).
apiClient.interceptors.request.use((config) => {
  if (!config.headers['Authorization']) {
    const token = localStorage.getItem('jarvis-admin:access_token')
    if (token) {
      config.headers['Authorization'] = `Bearer ${token}`
    }
  }
  return config
})

apiClient.interceptors.response.use(
  (response) => response,
  async (error) => {
    const original = error.config

    // A temp-password session (or one whose password a superuser reset meanwhile): show the
    // change-password screen instead of an error on every page.
    if (isPasswordChangeRequired(error)) {
      mustChangeFn?.()
      return Promise.reject(error)
    }

    // Don't intercept auth endpoints — let login/refresh errors pass through
    if (!original || original.url?.startsWith('/api/auth/')) {
      return Promise.reject(error)
    }

    if (error.response?.status === 401 && !original._retry && refreshFn) {
      original._retry = true
      const newToken = await refreshFn()
      if (newToken) {
        original.headers['Authorization'] = `Bearer ${newToken}`
        return apiClient(original)
      }
      logoutFn?.()
    }
    return Promise.reject(error)
  },
)
