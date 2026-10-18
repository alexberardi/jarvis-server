// The sandbox's APIs, straight to each service's port (sandbox.env), for what a test sets up
// and for checking that the UI changed the data, not only the page.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { env, stateFile, type Role } from './env'

export interface Res<T = any> {
  status: number
  body: T
}

/** call does one request. A 429 (the auth rate limit) waits out the minute and retries once. */
export async function call<T = any>(
  method: string,
  url: string,
  opts: { token?: string; headers?: Record<string, string>; body?: unknown } = {},
): Promise<Res<T>> {
  const headers: Record<string, string> = { ...opts.headers }
  if (opts.token) headers.Authorization = `Bearer ${opts.token}`
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  for (let attempt = 0; ; attempt++) {
    const r = await fetch(url, { method, headers, body: opts.body === undefined ? undefined : JSON.stringify(opts.body) })
    if (r.status === 429 && attempt === 0) {
      await new Promise((ok) => setTimeout(ok, 61_000))
      continue
    }
    const text = await r.text()
    let body: any = text
    try {
      body = text ? JSON.parse(text) : null
    } catch {
      // not JSON: keep the text
    }
    return { status: r.status, body }
  }
}

/** must is call that fails the test on an unexpected status. */
export async function must<T = any>(...args: Parameters<typeof call>): Promise<T> {
  const r = await call<T>(...args)
  if (r.status < 200 || r.status > 299) {
    throw new Error(`${args[0]} ${args[1]} → ${r.status}: ${JSON.stringify(r.body).slice(0, 300)}`)
  }
  return r.body
}

export async function login(email: string, password: string): Promise<string> {
  const b = await must('POST', `${env('SBX_AUTH')}/auth/login`, { body: { email, password } })
  return b.access_token
}

/**
 * tokenOf is the access token in a role's storage state (global-setup.ts signed it in), so
 * API calls as a seeded user cost no login against the rate limit.
 */
export function tokenOf(role: Role): string {
  const state = JSON.parse(readFileSync(stateFile(role), 'utf8'))
  for (const o of state.origins ?? []) {
    const t = o.localStorage?.find((e: { name: string }) => e.name === 'jarvis-admin:access_token')
    if (t) return t.value
  }
  throw new Error(`no access token in ${stateFile(role)}`)
}

/** asSuperuser is a bearer token for the seeded superuser. */
export async function asSuperuser(): Promise<string> {
  return tokenOf('superuser')
}

/** householdOf is the household a token is scoped to (its JWT's claim, else the first membership). */
export async function householdOf(token: string): Promise<string> {
  const claims = JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString('utf8'))
  if (claims.household_id) return claims.household_id
  const hh = await must<{ id: string }[]>('GET', `${env('SBX_AUTH')}/households`, { token })
  return hh[0].id
}

export interface User {
  id: number
  email: string
  password: string
  /** An access token scoped to householdId. */
  token: string
  refreshToken: string
  householdId: string
  /** The user as GET /auth/me returns it (what the SPA keeps in localStorage). */
  me: Record<string, unknown>
}

let seq = 0
/** unique is a name no other test or earlier run on this sandbox used. */
export function unique(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${(seq++).toString(36)}`
}

/**
 * newUser registers a throwaway account with a household of its own (it is that household's
 * admin), for tests that change a user or need data nobody else sees. Registration needs an
 * invite, so the superuser makes a lobby household, invites them, and deletes it again once
 * they have their own.
 */
export async function newUser(prefix = 'e2e', householdName = `${prefix} home`): Promise<User> {
  const auth = env('SBX_AUTH')
  const su = await asSuperuser()
  const email = `${unique(prefix)}@jarvisautomation.io`
  const password = 'E2ePass123!'
  const lobby = await createHousehold(su, unique('e2e lobby'))
  const inv = await must('POST', `${auth}/households/${lobby}/invites`, {
    token: su,
    body: { default_role: 'member', max_uses: 1, expires_in_days: 1 },
  })
  const reg = await must('POST', `${auth}/auth/register`, {
    body: { email, username: email.split('@')[0].replace(/[^a-z0-9]/g, ''), password, invite_code: inv.code },
  })
  const hh = await createHousehold(reg.access_token, householdName)
  await must('POST', `${auth}/households/${lobby}/leave`, { token: reg.access_token })
  await must('DELETE', `${auth}/households/${lobby}`, { token: su })
  const token = await switchHousehold(reg.access_token, hh)
  const me = await must('GET', `${auth}/auth/me`, { token })
  return { id: me.id, email, password, token, refreshToken: reg.refresh_token, householdId: hh, me }
}

/** switchHousehold is an access token for another of the user's households. */
export async function switchHousehold(token: string, householdId: string): Promise<string> {
  const b = await must('POST', `${env('SBX_AUTH')}/auth/switch-household`, { token, body: { household_id: householdId } })
  return b.access_token
}

export async function createHousehold(token: string, name: string): Promise<string> {
  const b = await must('POST', `${env('SBX_AUTH')}/households`, { token, body: { name } })
  return b.id
}

let app: { id: string; key: string } | undefined
/** appHeaders are an app client's credentials (made once per run with the auth admin token). */
export async function appHeaders(): Promise<Record<string, string>> {
  if (!app) {
    const id = unique('e2e-app')
    const b = await must('POST', `${env('SBX_AUTH')}/admin/app-clients`, {
      headers: { 'X-Jarvis-Admin-Token': env('SBX_AUTH_ADMIN_TOKEN') },
      body: { app_id: id, name: 'E2E suite' },
    })
    app = { id, key: b.key }
  }
  return { 'X-Jarvis-App-Id': app.id, 'X-Jarvis-App-Key': app.key }
}

export interface InboxItem {
  id: string
  title: string
  body: string
  is_read: boolean
  household_id: string
}

/** createInboxItem posts an item as a service would (a research result, say). */
export async function createInboxItem(
  to: { id: number; householdId: string },
  item: { title: string; body?: string; summary?: string; category?: string; metadata?: Record<string, unknown> },
): Promise<InboxItem> {
  return must('POST', `${env('SBX_NOTIFICATIONS')}/api/v0/inbox`, {
    headers: await appHeaders(),
    body: { user_id: to.id, household_id: to.householdId, source_service: 'e2e', category: 'deep_research', summary: '', body: '', ...item },
  })
}

/** inbox is the token's household's items for its user, as the inbox page lists them. */
export async function inbox(token: string): Promise<InboxItem[]> {
  return must('GET', `${env('SBX_NOTIFICATIONS')}/api/v0/inbox?limit=100`, { token })
}

export interface Memory {
  id: string
  content: string
}

export async function memories(token: string): Promise<Memory[]> {
  const hh = await householdOf(token)
  const b = await must('GET', `${env('SBX_CC')}/api/v0/mobile/memories?household_id=${hh}`, { token })
  return Array.isArray(b) ? b : (b.memories ?? b.items ?? [])
}

/**
 * provisionNode registers a node in a household the way a device pairs (a provisioning token,
 * then the node redeems it). kind 'desktop' makes it a desktop agent, which Add-ons lists.
 */
export async function provisionNode(
  token: string,
  householdId: string,
  node: { id: string; name: string; room: string; kind?: 'desktop' | 'node' },
): Promise<string> {
  const cc = env('SBX_CC')
  const t = await must('POST', `${cc}/api/v0/provisioning/token`, {
    token,
    body: { household_id: householdId, node_id: node.id, room: node.room, name: node.name },
  })
  const r = await must('POST', `${cc}/api/v0/nodes/register`, {
    body: { node_id: node.id, provisioning_token: t.token, room: node.room, ...(node.kind ? { node_kind: node.kind } : {}) },
  })
  return r.node_key
}

/** setSetting writes an install-wide setting through the service that owns it (as the superuser). */
export async function setSetting(serviceUrl: string, key: string, value: unknown): Promise<void> {
  await must('PUT', `${serviceUrl}/settings/${key}`, { token: await asSuperuser(), body: { value } })
}

/** useScript makes the scripted model answer from another script (e2e/llm/, docs/decisions/2026-10-17-scripted-llm.md). */
export async function useScript(rel: string): Promise<void> {
  await must('POST', `${env('SBX_SCRIPTLLM')}/control/script`, { body: { path: rel } })
}

/** modelCalls is every call the model got so far (the tap's llm.jsonl), oldest first. */
export function modelCalls(): any[] {
  let text = ''
  try {
    text = readFileSync(env('SBX_LLM_LOG'), 'utf8')
  } catch {
    return []
  }
  return text
    .split('\n')
    .filter(Boolean)
    .map((l) => JSON.parse(l))
}

export const repoRoot = path.resolve(import.meta.dirname, '../../..')
