// The login form, through tools/screenshots' signIn (one copy of how to fill it in).
import type { Page } from '@playwright/test'
// @ts-expect-error: a JavaScript module with JSDoc types, outside this package
import { signIn } from '../../../tools/screenshots/signin.mjs'

/**
 * signInUI logs in through the form. Login, register and refresh share one per-IP limit (30 a
 * minute, internal/modules/auth guards.go) and every test comes from 127.0.0.1, so a refusal
 * for that waits out the minute once and tries again.
 */
export async function signInUI(page: Page, admin: string, email: string, password: string): Promise<void> {
  try {
    await signIn(page, admin, email, password)
  } catch (err) {
    if (!/too many requests/i.test(String(err))) throw err
    await page.waitForTimeout(61_000)
    await signIn(page, admin, email, password)
  }
}

/** submitLogin fills the form and submits it without waiting for a session (for refusals). */
export async function submitLogin(page: Page, email: string, password: string): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

/**
 * pressSignIn submits the filled-in form for a sign-in that should succeed. A refusal for the
 * rate limit (see signInUI) waits out the minute once and submits again.
 */
export async function pressSignIn(page: Page): Promise<void> {
  const button = page.getByRole('button', { name: 'Sign in' })
  await button.click()
  const limited = page.getByRole('alert').filter({ hasText: /too many requests/i })
  const outcome = await Promise.race([
    page.waitForURL((u) => !u.pathname.endsWith('/login')).then(() => 'in' as const),
    limited.waitFor().then(() => 'limited' as const),
  ])
  if (outcome === 'limited') {
    await page.waitForTimeout(61_000)
    await button.click()
  }
}
