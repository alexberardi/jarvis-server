// The suite's test(): console errors and page errors of every page a test opens are recorded
// (the report's console_errors), and asUser opens a signed-in browser for a throwaway user.
import { test as base, expect, type Browser, type BrowserContext, type Page } from '@playwright/test'
import type { User } from './api'
import { env } from './env'

type Fixtures = {
  consoleErrors: string[]
  /**
   * asUser is a new browser context signed in as a throwaway user: their session goes
   * straight into localStorage, as a sign-in leaves it. The form itself is auth.spec.ts's;
   * this keeps the run under the auth rate limit (30 logins a minute).
   */
  asUser: (u: User) => Promise<Page>
  /** freshPage is a new browser context with no session. */
  freshPage: () => Promise<Page>
}

export function watch(page: Page, into: string[]): void {
  page.on('console', (m) => {
    if (m.type() === 'error') into.push(`${new URL(page.url()).pathname}: ${m.text()}`)
  })
  page.on('pageerror', (e) => into.push(`${new URL(page.url()).pathname}: uncaught ${e.message}`))
}

type StorageState = Exclude<Parameters<Browser['newContext']>[0], undefined>['storageState']

async function open(browser: Browser, contexts: BrowserContext[], errors: string[], storageState?: StorageState): Promise<Page> {
  const ctx = await browser.newContext({ baseURL: env('SBX_ADMIN'), viewport: { width: 1440, height: 900 }, storageState })
  contexts.push(ctx)
  const page = await ctx.newPage()
  watch(page, errors)
  return page
}

export const test = base.extend<Fixtures>({
  consoleErrors: [
    // Through the context, so a test that never uses the default page does not open one
    // (it would be an extra blank failure screenshot).
    async ({ context }, use, testInfo) => {
      const errors: string[] = []
      context.on('page', (p) => watch(p, errors))
      await use(errors)
      if (errors.length) {
        await testInfo.attach('console-errors', { body: errors.join('\n'), contentType: 'text/plain' })
      }
    },
    { auto: true },
  ],
  asUser: async ({ browser, consoleErrors }, use) => {
    const contexts: BrowserContext[] = []
    await use(async (u) => {
      const localStorage = [
        { name: 'jarvis-admin:access_token', value: u.token },
        { name: 'jarvis-admin:refresh_token', value: u.refreshToken },
        { name: 'jarvis-admin:user', value: JSON.stringify(u.me) },
      ]
      return open(browser, contexts, consoleErrors, { cookies: [], origins: [{ origin: env('SBX_ADMIN'), localStorage }] })
    })
    for (const c of contexts) await c.close()
  },
  freshPage: async ({ browser, consoleErrors }, use) => {
    const contexts: BrowserContext[] = []
    await use(() => open(browser, contexts, consoleErrors))
    for (const c of contexts) await c.close()
  },
})

export { expect }

/** sidebar is the main navigation's links, by visible label. */
export function sidebar(page: Page) {
  return page.locator('#admin-sidebar nav')
}
