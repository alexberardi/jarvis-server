import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'

import * as authApi from '@/api/auth'
import { setAuthToken, setLogoutFunction, setRefreshFunction } from '@/api/client'
import type { AuthUser, TokenResponse } from '@/api/auth'
import { clearSetupToken, getSetupToken } from '@/auth/setupToken'
import { errorMessage, errorStatus } from '@/lib/errors'

interface AuthState {
  user: AuthUser | null
  accessToken: string | null
  refreshToken: string | null
  isAuthenticated: boolean
  isLoading: boolean
  error: string | null
}

/**
 * SetupResult tells the caller why first-superuser setup failed. `tokenProblem` is a 401
 * (no token) or 403 (wrong token): the UI then asks the operator to paste the token from the
 * setup-token file (AD2).
 */
export type SetupResult =
  | { ok: true }
  | { ok: false; tokenProblem: boolean; status?: number; message: string }

interface AuthContextValue {
  state: AuthState
  /** True when the signed-in user holds a temporary password and must change it (O4). */
  mustChangePassword: boolean
  login: (email: string, password: string) => Promise<void>
  setup: (email: string, password: string, username?: string) => Promise<SetupResult>
  changePassword: (currentPassword: string, newPassword: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined)

const ACCESS_KEY = 'jarvis-admin:access_token'
const REFRESH_KEY = 'jarvis-admin:refresh_token'
const USER_KEY = 'jarvis-admin:user'

const REFRESH_INTERVAL_MS = 10 * 60 * 1000

const signedOut: AuthState = {
  user: null,
  accessToken: null,
  refreshToken: null,
  isAuthenticated: false,
  isLoading: false,
  error: null,
}

/** userFrom folds the top-level must_change_password into the stored user. */
function userFrom(res: TokenResponse): AuthUser {
  return {
    ...res.user,
    must_change_password: Boolean(res.must_change_password ?? res.user.must_change_password),
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>(() => {
    const storedAccess = localStorage.getItem(ACCESS_KEY)
    const storedRefresh = localStorage.getItem(REFRESH_KEY)
    const storedUser = localStorage.getItem(USER_KEY)

    if (storedAccess && storedRefresh && storedUser) {
      try {
        const user = JSON.parse(storedUser) as AuthUser
        setAuthToken(storedAccess)
        return {
          user,
          accessToken: storedAccess,
          refreshToken: storedRefresh,
          isAuthenticated: true,
          isLoading: false,
          error: null,
        }
      } catch {
        // Corrupted stored data — fall through to defaults
      }
    }
    return signedOut
  })

  /** adopt stores a token pair and its user: the one place a session starts (O5). */
  const adopt = useCallback((res: TokenResponse) => {
    const user = userFrom(res)
    localStorage.setItem(ACCESS_KEY, res.access_token)
    localStorage.setItem(REFRESH_KEY, res.refresh_token)
    localStorage.setItem(USER_KEY, JSON.stringify(user))
    setAuthToken(res.access_token)
    setState({
      user,
      accessToken: res.access_token,
      refreshToken: res.refresh_token,
      isAuthenticated: true,
      isLoading: false,
      error: null,
    })
  }, [])

  const logout = useCallback(() => {
    const refreshToken = localStorage.getItem(REFRESH_KEY)
    if (refreshToken) {
      // Best effort: revoke the refresh token's family server-side.
      authApi.logout(refreshToken).catch(() => {})
    }
    localStorage.removeItem(ACCESS_KEY)
    localStorage.removeItem(REFRESH_KEY)
    localStorage.removeItem(USER_KEY)
    // Leftovers from the old setup wizard, which wrote un-namespaced keys.
    localStorage.removeItem('access_token')
    localStorage.removeItem('refresh_token')
    setAuthToken(null)
    setState(signedOut)
  }, [])

  const refreshAccessToken = useCallback(async (): Promise<string | null> => {
    const storedRefresh = localStorage.getItem(REFRESH_KEY)
    if (!storedRefresh) return null

    try {
      const res = await authApi.refresh(storedRefresh)
      const newAccess = res.access_token
      const newRefresh = res.refresh_token ?? storedRefresh

      localStorage.setItem(ACCESS_KEY, newAccess)
      localStorage.setItem(REFRESH_KEY, newRefresh)
      setAuthToken(newAccess)

      setState((prev) => ({
        ...prev,
        accessToken: newAccess,
        refreshToken: newRefresh,
      }))

      return newAccess
    } catch {
      logout()
      return null
    }
  }, [logout])

  // Register refresh/logout functions with the axios interceptor
  useEffect(() => {
    setRefreshFunction(refreshAccessToken)
    setLogoutFunction(logout)
  }, [refreshAccessToken, logout])

  // Periodic token refresh
  useEffect(() => {
    if (!state.isAuthenticated) return
    const timer = setInterval(() => {
      refreshAccessToken()
    }, REFRESH_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [state.isAuthenticated, refreshAccessToken])

  const login = useCallback(
    async (email: string, password: string) => {
      setState((prev) => ({ ...prev, error: null, isLoading: true }))
      try {
        const res = await authApi.login(email, password)
        // UX gate only — the real security boundary is server-side
        if (!res.user.is_superuser) {
          setState((prev) => ({
            ...prev,
            isLoading: false,
            error: 'Admin access required. This account is not a superuser.',
          }))
          return
        }
        adopt(res)
      } catch (err: unknown) {
        setState((prev) => ({ ...prev, isLoading: false, error: errorMessage(err, 'Login failed') }))
      }
    },
    [adopt],
  )

  const setup = useCallback(
    async (email: string, password: string, username?: string): Promise<SetupResult> => {
      setState((prev) => ({ ...prev, error: null, isLoading: true }))
      try {
        const res = await authApi.setup(email, password, username, getSetupToken())
        clearSetupToken()
        adopt(res)
        return { ok: true }
      } catch (err: unknown) {
        const status = errorStatus(err)
        const message = errorMessage(err, 'Setup failed')
        setState((prev) => ({ ...prev, isLoading: false, error: message }))
        return { ok: false, tokenProblem: status === 401 || status === 403, status, message }
      }
    },
    [adopt],
  )

  const changePassword = useCallback(
    async (currentPassword: string, newPassword: string) => {
      // Throws on failure; the page shows errorMessage(err). The response is a fresh pair:
      // every other session was revoked.
      const res = await authApi.changePassword(currentPassword, newPassword)
      adopt(res)
    },
    [adopt],
  )

  const mustChangePassword = Boolean(state.user?.must_change_password)

  const value = useMemo(
    () => ({ state, mustChangePassword, login, setup, changePassword, logout }),
    [state, mustChangePassword, login, setup, changePassword, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
