/**
 * Per-user client state outside the auth keys (webapp 01 G13): the open chat conversation
 * (sessionStorage), the remembered chat node and household (localStorage) and cached tool
 * lists. Logging out, or a different user signing in, clears all of it so nothing leaks to
 * the next person at a shared computer. The auth keys themselves are AuthContext's.
 */
export const HOUSEHOLD_KEY = 'jarvis-admin:household'
/** Prefix of every per-user localStorage key besides the household. */
export const USER_LOCAL_PREFIX = 'jarvis-admin:u:'
/** Prefix of every sessionStorage key the app writes. */
export const SESSION_PREFIX = 'jarvis-admin:'

function keysOf(store: Storage): string[] {
  const out: string[] = []
  for (let i = 0; i < store.length; i++) {
    const k = store.key(i)
    if (k !== null) out.push(k)
  }
  return out
}

export function clearSessionState(): void {
  try {
    for (const k of keysOf(sessionStorage)) {
      if (k.startsWith(SESSION_PREFIX)) sessionStorage.removeItem(k)
    }
  } catch {
    // Storage blocked: nothing was kept there either.
  }
  try {
    localStorage.removeItem(HOUSEHOLD_KEY)
    for (const k of keysOf(localStorage)) {
      if (k.startsWith(USER_LOCAL_PREFIX)) localStorage.removeItem(k)
    }
  } catch {
    // As above.
  }
}

/** householdOf reads the household_id claim of an access token ("" when it has none). */
export function householdOf(token: string | null | undefined): string {
  if (!token) return ''
  const part = token.split('.')[1]
  if (!part) return ''
  try {
    const b64 = part.replace(/-/g, '+').replace(/_/g, '/')
    const json = JSON.parse(atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4))) as { household_id?: unknown }
    return typeof json.household_id === 'string' ? json.household_id : ''
  } catch {
    return ''
  }
}

/** nameOf is what the app calls a user: the display name, else the username, else the email. */
export function nameOf(user: { display_name?: string | null; username?: string; email: string } | null): string {
  if (!user) return ''
  return user.display_name?.trim() || user.username?.trim() || user.email
}
