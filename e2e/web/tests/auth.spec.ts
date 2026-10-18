// Signing in and out, a refused password, and the forced change after a temporary password.
import { call, asSuperuser, login, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { pressSignIn, submitLogin } from '../helpers/login'

const ACCESS = 'jarvis-admin:access_token'
const REFRESH = 'jarvis-admin:refresh_token'
const token = (page: import('@playwright/test').Page, key = ACCESS) => page.evaluate((k) => localStorage.getItem(k), key)

test('an anonymous visitor is sent to the login page', async ({ freshPage }) => {
  const page = await freshPage()
  await page.goto('/inbox')
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
})

test('the superuser signs in and gets the admin pages', async ({ freshPage }) => {
  const page = await freshPage()
  await submitLogin(page, env('SBX_EMAIL'), env('SBX_PASSWORD'))
  // A superuser goes through /setup, which resumes the wizard while the install's setup is
  // unfinished (the seeded sandbox's is) and otherwise forwards home.
  await expect(page).not.toHaveURL(/\/login$/)
  await expect.poll(() => token(page)).toBeTruthy()
  await page.goto('/chat')
  await expect(page.locator('#admin-sidebar nav').getByRole('link', { name: 'Dashboard' })).toBeVisible()
})

test('a wrong password is refused, then the right one signs in', async ({ freshPage }) => {
  const u = await newUser('badpw')
  const page = await freshPage()
  await submitLogin(page, u.email, 'not-the-password')
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByRole('alert')).not.toBeEmpty()
  await expect(page).toHaveURL(/\/login$/)
  expect(await token(page)).toBeNull()

  await page.getByLabel('Password').fill(u.password)
  await pressSignIn(page)
  await expect(page).toHaveURL(/\/chat$/)
  expect(await token(page)).toBeTruthy()
})

test('logging out ends the session', async ({ asUser }) => {
  const u = await newUser('logout')
  const page = await asUser(u)
  await page.goto('/chat')
  const refresh = await token(page, REFRESH)
  expect(refresh).toBeTruthy()

  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(await token(page)).toBeNull()
  // The server forgot the session too: its refresh token no longer works.
  const r = await call('POST', `${env('SBX_AUTH')}/auth/refresh`, { body: { refresh_token: refresh } })
  expect(r.status).toBe(401)
  // And the app is closed again.
  await page.goto('/inbox')
  await expect(page).toHaveURL(/\/login$/)
})

test('a temporary password must be changed before anything else', async ({ freshPage }) => {
  const u = await newUser('temppw')
  // The superuser issues a temporary password (Users page → Reset password does the same).
  const temp = 'Temp-Pass-123'
  const r = await call('POST', `${env('SBX_ADMIN')}/api/admin/users/${u.id}/temp-password`, {
    token: await asSuperuser(),
    body: { temp_password: temp },
  })
  expect(r.status, JSON.stringify(r.body)).toBe(200)
  expect(r.body.must_change_password).toBe(true)

  const page = await freshPage()
  await submitLogin(page, u.email, temp)
  await expect(page).toHaveURL(/\/change-password$/)
  await expect(page.getByText('You signed in with a temporary password')).toBeVisible()
  // Every other page sends them back here.
  await page.goto('/inbox')
  await expect(page).toHaveURL(/\/change-password$/)

  const next = 'Chosen-Pass-456'
  await page.getByLabel('Temporary password').fill(temp)
  await page.getByLabel('New password', { exact: true }).fill(next)
  await page.getByLabel('Confirm new password').fill(next)
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page).toHaveURL(/\/chat$/)

  // Not asked again: the new password signs in straight to the app; the temporary one is dead.
  await page.getByRole('button', { name: 'Log out' }).click()
  await submitLogin(page, u.email, next)
  await expect(page).toHaveURL(/\/chat$/)
  expect((await call('POST', `${env('SBX_AUTH')}/auth/login`, { body: { email: u.email, password: temp } })).status).toBe(401)
  expect(await login(u.email, next)).toBeTruthy()
})
