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
import type { AuthUser } from '@/api/auth'

interface AuthState {
  user: AuthUser | null
  accessToken: string | null
  refreshToken: string | null
  isAuthenticated: boolean
  isLoading: boolean
  error: string | null
}

interface AuthContextValue {
  state: AuthState
  login: (email: string, password: string) => Promise<void>
  setup: (email: string, password: string, username?: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined)

const ACCESS_KEY = 'jarvis-admin:access_token'
const REFRESH_KEY = 'jarvis-admin:refresh_token'
const USER_KEY = 'jarvis-admin:user'

const REFRESH_INTERVAL_MS = 10 * 60 * 1000

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

    return {
      user: null,
      accessToken: null,
      refreshToken: null,
      isAuthenticated: false,
      isLoading: false,
      error: null,
    }
  })

  const logout = useCallback(() => {
    localStorage.removeItem(ACCESS_KEY)
    localStorage.removeItem(REFRESH_KEY)
    localStorage.removeItem(USER_KEY)
    setAuthToken(null)
    setState({
      user: null,
      accessToken: null,
      refreshToken: null,
      isAuthenticated: false,
      isLoading: false,
      error: null,
    })
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

  const login = useCallback(async (email: string, password: string) => {
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

      localStorage.setItem(ACCESS_KEY, res.access_token)
      localStorage.setItem(REFRESH_KEY, res.refresh_token)
      localStorage.setItem(USER_KEY, JSON.stringify(res.user))
      setAuthToken(res.access_token)

      setState({
        user: res.user,
        accessToken: res.access_token,
        refreshToken: res.refresh_token,
        isAuthenticated: true,
        isLoading: false,
        error: null,
      })
    } catch (err: unknown) {
      const message =
        (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ??
        'Login failed'
      setState((prev) => ({
        ...prev,
        isLoading: false,
        error: message,
      }))
    }
  }, [])

  const setup = useCallback(async (email: string, password: string, username?: string) => {
    setState((prev) => ({ ...prev, error: null, isLoading: true }))

    try {
      const res = await authApi.setup(email, password, username)

      localStorage.setItem(ACCESS_KEY, res.access_token)
      localStorage.setItem(REFRESH_KEY, res.refresh_token)
      localStorage.setItem(USER_KEY, JSON.stringify(res.user))
      setAuthToken(res.access_token)

      setState({
        user: res.user,
        accessToken: res.access_token,
        refreshToken: res.refresh_token,
        isAuthenticated: true,
        isLoading: false,
        error: null,
      })
    } catch (err: unknown) {
      const message =
        (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ??
        'Setup failed'
      setState((prev) => ({
        ...prev,
        isLoading: false,
        error: message,
      }))
    }
  }, [])

  const value = useMemo(() => ({ state, login, setup, logout }), [state, login, setup, logout])

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
