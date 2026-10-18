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

/** openSetting searches for the key and opens the module's card (the search leaves it collapsed). */
async function openSetting(page: Page) {
  await page.getByPlaceholder('Search settings...').fill(KEY)
  await page.getByRole('button', { name: /Command center/ }).click()
}

async function setting(): Promise<Setting> {
  const b = await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=cc`, { token: tokenOf('superuser') })
  const s = b.services.flatMap((svc: { settings: Setting[] }) => svc.settings).find((x: Setting) => x.key === KEY)
  if (!s) throw new Error(`no ${KEY} in /api/settings/`)
  return s
}

const ownValue = (s: Setting, hh: string) => s.household_values?.find((v) => v.household_id === hh)?.value

test.describe('admin settings', () => {
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
