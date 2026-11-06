// /admin/settings: a household's own value of a household-scoped setting, edited here, is saved
// on the server and survives a reload; "Use default" removes it. A throwaway household, so the
// seeded one's settings never change.
import type { Page } from '@playwright/test'
import { must, newUser, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const KEY = 'household.location'
const settingURL = (hh: string) => `${env('SBX_ADMIN')}/api/settings/cc/${KEY}?household_id=${hh}`

interface Setting {
  key: string
  value: unknown
  household_values?: { household_id: string; household_name: string; value: unknown }[]
}

/** Search may finish before settings load, in which case the first matching card starts open. */
async function openSetting(page: Page, key = KEY) {
  await page.getByLabel('Search settings').fill(key)
  const card = page.getByRole('button', { name: /Command center/ })
  await expect(card).toBeVisible()
  if (await card.getAttribute('aria-expanded') === 'false') await card.click()
  await expect(card).toHaveAttribute('aria-expanded', 'true')
}

async function setting(key = KEY): Promise<Setting> {
  const b = await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=cc`, { token: tokenOf('superuser') })
  const s = b.services.flatMap((svc: { settings: Setting[] }) => svc.settings).find((x: Setting) => x.key === key)
  if (!s) throw new Error(`no ${key} in /api/settings/`)
  return s
}

const ownValue = (s: Setting, hh: string) => s.household_values?.find((v) => v.household_id === hh)?.value

test.describe('admin settings', () => {
  test('inbound household setup: create the first override, reload, disable and use default', async ({ page }) => {
    const key = 'phone.inbound.enabled'
    const name = `Phone home ${Date.now().toString(36)}`
    const u = await newUser('inbound-settings', name)
    const before = await setting(key)
    expect(ownValue(before, u.householdId)).toBeUndefined()

    await page.goto('/admin/settings')
    await openSetting(page, key)
    await page.getByRole('button', { name: 'Set for a household', exact: true }).click()
    await page.getByRole('combobox', { name: 'Household', exact: true }).selectOption(u.householdId)
    const editor = page.getByRole('group', { name: `Set ${key} for a household` })
    await editor.getByRole('switch', { name: key, exact: true }).check()
    await editor.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText(`Updated for ${name}`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBe(true)

    await page.reload()
    await openSetting(page, key)
    const row = page.getByTestId(`household-value-${u.householdId}`)
    await expect(row).toContainText(name)
    await expect(row).toContainText('true')
    await row.getByRole('button', { name: `Edit ${name}`, exact: true }).click()
    await row.getByRole('switch', { name: key, exact: true }).uncheck()
    await row.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText(`Updated for ${name}`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBe(false)

    await row.getByRole('button', { name: 'Use default', exact: true }).click()
    await expect(page.getByText(`${name} now uses the default`)).toBeVisible()
    await expect(row).toHaveCount(0)
    const after = await setting(key)
    expect(after.value).toEqual(before.value)
    expect(after.household_values).toEqual(before.household_values)
  })

  test("a household's own value: edited, kept across a reload, then back to the default", async ({ page }) => {
    const u = await newUser('settings', `Settings home ${Date.now().toString(36)}`)
    const name = (await must('GET', `${env('SBX_AUTH')}/households/${u.householdId}`, { token: u.token })).name
    const defaultBefore = (await setting()).value
    // The household sets its own value (as the mobile app would); the admin then edits it.
    await must('PUT', settingURL(u.householdId), { token: tokenOf('superuser'), body: { value: 'Springfield' } })

    await page.goto('/admin/settings')
    await openSetting(page)
    const row = page.getByTestId(`household-value-${u.householdId}`)
    await expect(row).toContainText(name)
    await expect(row).toContainText('Springfield')

    await row.getByRole('button', { name: `Edit ${name}` }).click()
    await row.getByRole('textbox').fill('Shelbyville')
    await row.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText(`Updated for ${name}`)).toBeVisible()
    expect(ownValue(await setting(), u.householdId)).toBe('Shelbyville')

    await page.reload()
    await openSetting(page)
    await expect(page.getByTestId(`household-value-${u.householdId}`)).toContainText('Shelbyville')

    await page.getByTestId(`household-value-${u.householdId}`).getByRole('button', { name: 'Use default' }).click()
    await expect(page.getByText(`${name} now uses the default`)).toBeVisible()
    await expect(page.getByTestId(`household-value-${u.householdId}`)).toHaveCount(0)
    const after = await setting()
    expect(ownValue(after, u.householdId)).toBeUndefined()
    // The default itself never changed.
    expect(after.value).toEqual(defaultBefore)
  })
})
