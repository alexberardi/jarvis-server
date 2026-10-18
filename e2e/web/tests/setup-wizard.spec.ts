// First-run setup on the empty sandbox (scripts/sandbox --profile empty, E2E_SETUP_*): the
// wizard creates the superuser, every model step is skipped (no download), and afterwards the
// install is set up: the new superuser signs in and the setup token is spent.
import { call } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { signInUI } from '../helpers/login'

test('the wizard sets up an empty install', async ({ freshPage }) => {
  const admin = env('E2E_SETUP_ADMIN')
  const before = await call('GET', `${admin}/api/setup/state`)
  expect(before.body.needs_superuser, 'the setup sandbox is empty').toBe(true)

  // Before setup, every page leads to the wizard.
  const visitor = await freshPage()
  await visitor.goto(`${admin}/login`)
  await expect(visitor).toHaveURL(/\/setup$/)

  // The link jarvisd prints carries the one-time token (a new page: the SPA reads it on load).
  const page = await freshPage()
  await page.goto(`${admin}/setup#token=${env('E2E_SETUP_TOKEN')}`)
  const step = (name: string) => expect(page.getByRole('heading', { name }).first()).toBeVisible()
  const click = (name: string) => page.getByRole('button', { name, exact: true }).last().click()

  await step('Check this machine')
  await click('Continue')
  await step('Create Account')
  // The token came with the link: the form does not ask for it.
  await expect(page.getByLabel('Setup token')).toHaveCount(0)
  const email = `owner-${Date.now().toString(36)}@jarvisautomation.io`
  const password = 'Owner-Pass-123'
  await page.getByLabel('Display name').fill('Owner')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByLabel('Confirm password').fill(password)
  await click('Create Superuser Account')

  await step('Hardware')
  await click('Looks good')
  for (const [name, skip] of [
    ['Language model', 'Skip for now'],
    ['Speech-to-text', 'Skip for now'],
    ['Voice', 'Skip for now'],
    ['Voice recognition', 'Skip'],
    ['Memory & search', 'Skip'],
  ]) {
    await step(name)
    await click(skip)
  }
  await step('Privacy')
  await click('Continue')
  await step('Also use Jarvis Automation on this computer')
  await click('Skip for now')
  await step('Jarvis is set up')
  await click('Go to the dashboard')
  await expect(page).toHaveURL(/\/admin\/dashboard$/)
  await expect(page.getByRole('heading', { name: 'Dashboard', exact: true })).toBeVisible()

  // The install is set up: no superuser needed, nothing was downloaded, the token is spent.
  const after = await call('GET', `${admin}/api/setup/state`)
  expect(after.body.needs_superuser).toBe(false)
  const installs = await call('GET', `${admin}/api/llm/v1/models/installs`, {
    token: await page.evaluate(() => localStorage.getItem('jarvis-admin:access_token') ?? ''),
  })
  expect(installs.status).toBe(200)
  expect(JSON.stringify(installs.body)).not.toMatch(/"state":\s*"(queued|downloading|running)"/)
  const again = await call('POST', `${admin}/api/auth/setup`, {
    headers: { 'X-Jarvis-Setup-Token': env('E2E_SETUP_TOKEN') },
    body: { email: 'second@jarvisautomation.io', username: 'second', password },
  })
  expect(again.status).toBeGreaterThanOrEqual(400)

  // And the new superuser signs in from scratch.
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  await signInUI(page, admin, email, password)
  await expect(page).not.toHaveURL(/\/login$/)
  await page.goto(`${admin}/admin/users`)
  await expect(page.getByText(email).first()).toBeVisible()
})
