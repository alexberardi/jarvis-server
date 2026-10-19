// Push notifications: every sandbox's relay.url points at tools/fakerelay, which records each
// push instead of sending it ($SBX_FAKERELAY/control/pushes). A test registers a fake Expo
// token for a user, makes something happen, and asserts the push the relay got
// (docs/decisions/2026-10-18-e2e-push.md).
import { expect } from '@playwright/test'
import { must, provisionNode, unique, type User } from './api'
import { env } from './env'

/** Push is one /v1/send the fake relay recorded (tools/fakerelay/server.go). */
export interface Push {
  seq: number
  household_id: string
  tokens: string[]
  title: string
  body: string
  data: Record<string, any>
  priority: string
}

/**
 * registerDevice registers a phone's push token for u, as the app does after sign-in. A
 * prefix of 'fail:' makes the relay answer DeviceNotRegistered for it (an uninstalled app).
 */
export async function registerDevice(u: User, prefix = 'ExponentPushToken'): Promise<string> {
  const token = prefix === 'fail:' ? `fail:${unique('gone')}` : `${prefix}[${unique('e2e')}]`
  await must('POST', `${env('SBX_NOTIFICATIONS')}/api/v0/tokens`, {
    token: u.token,
    body: { push_token: token, device_type: 'android', device_name: 'E2E phone' },
  })
  return token
}

/** activeTokens are u's devices that still get pushes. */
export async function activeTokens(u: User): Promise<string[]> {
  const b = await must<{ push_token: string }[]>('GET', `${env('SBX_NOTIFICATIONS')}/api/v0/tokens/me`, { token: u.token })
  return b.map((t) => t.push_token)
}

/** pushesTo is every push the relay got for a device token, oldest first. */
export async function pushesTo(token: string): Promise<Push[]> {
  const b = await must<{ pushes: Push[] }>('GET', `${env('SBX_FAKERELAY')}/control/pushes?token=${encodeURIComponent(token)}`)
  return b.pushes
}

/**
 * pushTo waits for the one push to token titled title (the delivery is a queued job, so it
 * lands a moment after the event) and returns it.
 */
export async function pushTo(token: string, title: string): Promise<Push> {
  let found: Push[] = []
  await expect
    .poll(
      async () => {
        found = (await pushesTo(token)).filter((p) => p.title === title)
        return found.length
      },
      { message: `a push titled ${JSON.stringify(title)} to ${token} (is relay.url the fake relay?)`, timeout: 20_000 },
    )
    .toBe(1)
  return found[0]
}

/** nodeOf is a node in u's household, with its X-API-Key, for what a Pi or a desktop agent sends. */
export async function nodeOf(u: User): Promise<Record<string, string>> {
  return (await nodeWithId(u)).headers
}

/** nodeWithId is nodeOf with the node's id too (a chat turn names the node it goes through). */
export async function nodeWithId(u: User): Promise<{ id: string; headers: Record<string, string> }> {
  const id = crypto.randomUUID()
  const key = await provisionNode(u.token, u.householdId, { id, name: 'E2E kitchen', room: 'kitchen' })
  return { id, headers: { 'X-API-Key': `${id}:${key}` } }
}
