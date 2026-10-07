/**
 * The first-run setup token (AD2).
 *
 * While no superuser exists, jarvisd writes `<home>/setup-token` and prints the link
 * `http://<lan-ip>:7710/setup#token=…`. The token sits in the URL fragment, so it never reaches a
 * server log. The SPA reads it once, strips it from the address bar (so it doesn't end up in
 * history, bookmarks or a screenshot), and keeps it for this tab only until setup succeeds.
 * `POST /api/auth/setup` sends it as `X-Jarvis-Setup-Token`.
 */

const STORAGE_KEY = 'jarvis-admin:setup-token'

export const SETUP_TOKEN_HEADER = 'X-Jarvis-Setup-Token'

let memoryToken: string | null = null

/**
 * captureSetupToken reads `#token=…` from the current URL when on /setup, remembers it and
 * removes it from the address bar. Call it before the router renders. Returns the token found.
 */
export function captureSetupToken(loc: Location = window.location, hist: History = window.history): string | null {
  if (loc.pathname.replace(/\/+$/, '') !== '/setup') return null
  const hash = loc.hash.startsWith('#') ? loc.hash.slice(1) : loc.hash
  if (!hash) return null
  const params = new URLSearchParams(hash)
  const token = params.get('token')?.trim()
  if (!token) return null

  setSetupToken(token)
  params.delete('token')
  const rest = params.toString()
  const url = loc.pathname + loc.search + (rest ? `#${rest}` : '')
  try {
    hist.replaceState(hist.state, '', url)
  } catch {
    // Some embedded browsers refuse replaceState; the token still works from memory.
  }
  return token
}

export function getSetupToken(): string | null {
  if (memoryToken) return memoryToken
  try {
    return sessionStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

export function setSetupToken(token: string): void {
  const t = token.trim()
  memoryToken = t || null
  try {
    if (t) sessionStorage.setItem(STORAGE_KEY, t)
    else sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // Storage blocked: memory only.
  }
}

export function clearSetupToken(): void {
  memoryToken = null
  try {
    sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // ignore
  }
}
