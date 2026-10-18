// The account page (display name) and changing your own password.
import { call, login, must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { submitLogin } from '../helpers/login'

test('the display name is saved and shown', async ({ asUser }) => {
  const u = await newUser('dname')
  const page = await asUser(u)
  await page.goto('/account')
  await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible()
  await expect(page.getByText(u.email)).toBeVisible()

  await page.getByLabel('Display name').fill('Quinn Tester')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('account-name')).toHaveText('Quinn Tester')
  const me = await must('GET', `${env('SBX_AUTH')}/auth/me`, { token: u.token })
  expect(me.display_name).toBe('Quinn Tester')

  // It survives a reload (it came from the server, not the form).
  await page.reload()
  await expect(page.getByTestId('account-name')).toHaveText('Quinn Tester')
})

test('changing the password: the old one stops working, the new one signs in', async ({ asUser }) => {
  const u = await newUser('chpw')
  const page = await asUser(u)
  await page.goto('/chat')
  await page.getByRole('link', { name: 'Change password' }).click()
  await expect(page).toHaveURL(/\/change-password$/)

  // The form checks before it sends.
  await page.getByLabel('Current password').fill(u.password)
  await page.getByLabel('New password', { exact: true }).fill('Another-Pass-1')
  await page.getByLabel('Confirm new password').fill('Another-Pass-2')
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByRole('alert')).toHaveText('Passwords do not match')

  const next = 'Another-Pass-1'
  await page.getByLabel('Confirm new password').fill(next)
  await page.getByRole('button', { name: 'Change password' }).click()
  await expect(page).toHaveURL(/\/chat$/)

  expect((await call('POST', `${env('SBX_AUTH')}/auth/login`, { body: { email: u.email, password: u.password } })).status).toBe(401)
  expect(await login(u.email, next)).toBeTruthy()
  await page.getByRole('button', { name: 'Log out' }).click()
  await submitLogin(page, u.email, next)
  await expect(page).toHaveURL(/\/chat$/)
})
