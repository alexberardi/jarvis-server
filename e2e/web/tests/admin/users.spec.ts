// /admin/users: every account with its households and roles as the server has them; a password
// reset (generated or chosen) that the user then signs in with and must change; a deactivated
// account (seeded: no API deactivates one) marked so and not resettable.
import type { Page } from '@playwright/test'
import { call, must, newUser, tokenOf, type User } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'
import { pressSignIn } from '../../helpers/login'

test.use({ storageState: stateFile('superuser') })

// scripts/seed-dev.py, rich profile.
const DEACTIVATED = 'former@jarvisautomation.io'

interface AdminUser {
  id: number
  email: string
  is_active: boolean
  is_superuser: boolean
  must_change_password: boolean
  households: { household_name: string; role: string }[]
}

const users = (): Promise<AdminUser[]> => must('GET', `${env('SBX_ADMIN')}/api/admin/users`, { token: tokenOf('superuser') })

/** reset runs the card's Reset password with an optional chosen password; returns the one shown. */
async function reset(page: Page, u: User, chosen?: string): Promise<string> {
  await page.goto('/admin/users')
  const card = page.getByTestId(`user-${u.id}`)
  await card.getByRole('button', { name: 'Reset password' }).click()
  if (chosen) await card.getByPlaceholder(/Custom temp password/).fill(chosen)
  await card.getByRole('button', { name: 'Reset password' }).click()
  await expect(card).toContainText(`Temporary password for ${u.email}`)
  return (await card.locator('code').first().textContent())!.trim()
}

/** signsInToChange checks the user's next sign-in, with password, lands on the forced change. */
async function signsInToChange(page: Page, u: User, password: string) {
  expect((await call('POST', `${env('SBX_AUTH')}/auth/login`, { body: { email: u.email, password: u.password } })).status).toBe(401)
  await page.goto('/login')
  await page.getByLabel('Email').fill(u.email)
  await page.getByLabel('Password').fill(password)
  await pressSignIn(page)
  await expect(page).toHaveURL(/\/change-password$/)
  await expect(page.getByText('You signed in with a temporary password')).toBeVisible()
}

test.describe('admin users', () => {
  test('lists every account with its households and roles', async ({ page }) => {
    const all = await users()
    await page.goto('/admin/users')
    for (const u of all) {
      const card = page.getByTestId(`user-${u.id}`)
      await expect(card).toContainText(u.email)
      for (const h of u.households) await expect(card).toContainText(`${h.household_name} (${h.role})`)
      await expect(card.getByText('Superuser', { exact: true })).toHaveCount(u.is_superuser ? 1 : 0)
      await expect(card.getByText('Deactivated', { exact: true })).toHaveCount(u.is_active ? 0 : 1)
    }
  })

  test('a generated temporary password: shown once, the user signs in with it and must change it', async ({ page, freshPage }) => {
    const u = await newUser('users-gen')
    const temp = await reset(page, u)
    expect(temp.length).toBeGreaterThanOrEqual(8)
    const card = page.getByTestId(`user-${u.id}`)
    await expect(card.getByText('Temp password', { exact: true })).toBeVisible()
    expect((await users()).find((x) => x.id === u.id)).toMatchObject({ must_change_password: true })
    await signsInToChange(await freshPage(), u, temp)
  })

  test('a chosen temporary password works the same way', async ({ page, freshPage }) => {
    const u = await newUser('users-own')
    const chosen = 'Chosen-Temp-42'
    expect(await reset(page, u, chosen)).toBe(chosen)
    await signsInToChange(await freshPage(), u, chosen)
  })

  test('a deactivated account is marked and cannot be reset', async ({ page }) => {
    const former = (await users()).find((u) => u.email === DEACTIVATED)
    expect(former, `${DEACTIVATED} (seed-dev.py --profile rich)`).toBeTruthy()
    expect(former!.is_active).toBe(false)
    await page.goto('/admin/users')
    const card = page.getByTestId(`user-${former!.id}`)
    await expect(card.getByText('Deactivated', { exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: 'Reset password' })).toBeDisabled()
    await expect(card.getByRole('button', { name: 'Reset password' })).toHaveAttribute('title', 'Reactivate the account first')
  })

  test('inviting, changing a role or disabling an account', async () => {
    test.skip(true, 'the web admin has none of these in v1 (AccountPage.tsx: invites and households stay in the mobile app); docs/decisions/2026-10-17-e2e-admin-pages.md')
  })
})
