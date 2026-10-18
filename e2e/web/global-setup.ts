// Signs each seeded role in once through the login form and keeps the session (localStorage)
// as a storage state, so tests start signed in: test.use({ storageState: stateFile('member') }).
import { chromium, type FullConfig } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import path from 'node:path'
import { env, roleEmail, ROLES, stateFile } from './helpers/env'
import { signInUI } from './helpers/login'

export default async function globalSetup(_config: FullConfig) {
  const admin = env('SBX_ADMIN')
  const browser = await chromium.launch()
  try {
    for (const role of ROLES) {
      const ctx = await browser.newContext()
      const page = await ctx.newPage()
      await signInUI(page, admin, roleEmail(role), env('SBX_PASSWORD'))
      await mkdir(path.dirname(stateFile(role)), { recursive: true })
      await ctx.storageState({ path: stateFile(role) })
      await ctx.close()
    }
  } finally {
    await browser.close()
  }
}
