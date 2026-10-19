// The household switches on the Account page: watches and errand autonomy, off by default,
// flipped by a household admin and only shown to a member.
import { call, joinHousehold, must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

const SWITCHES = [
  { key: 'watches.enabled', label: 'Watches' },
  { key: 'errands.autonomous_enabled', label: 'Errand autonomy' },
]

const settingsURL = (hh: string) => `${env('SBX_CC')}/api/v0/mobile/household/${hh}/settings`

for (const s of SWITCHES) {
  test(`a household admin turns ${s.label} on and it stays on`, async ({ asUser }) => {
    const admin = await newUser('hhset')
    const page = await asUser(admin)
    await page.goto('/account')
    const section = page.getByRole('region', { name: /Household/ })
    const sw = section.getByRole('switch', { name: s.label })
    await expect(sw).toBeEnabled()
    await expect(sw).not.toBeChecked()
    await expect(sw).toHaveAccessibleDescription(/Off by default\./)

    await sw.click()
    await expect(page.getByText('Saved')).toBeVisible()
    await expect(sw).toBeChecked()
    const after = await must('GET', settingsURL(admin.householdId), { token: admin.token })
    expect(after.settings[s.key]).toBe(true)

    // It came from the server, not the form.
    await page.reload()
    await expect(page.getByRole('switch', { name: s.label })).toBeChecked()

    await page.getByRole('switch', { name: s.label }).click()
    await expect(page.getByRole('switch', { name: s.label })).not.toBeChecked()
    await expect
      .poll(async () => (await must('GET', settingsURL(admin.householdId), { token: admin.token })).settings[s.key])
      .toBe(false)
  })
}

test('a member sees both switches but cannot change them', async ({ asUser }) => {
  const admin = await newUser('hhadm')
  await must('PUT', `${settingsURL(admin.householdId)}/watches.enabled`, { token: admin.token, body: { value: true } })
  const member = await joinHousehold(admin, await newUser('hhmem'))

  const page = await asUser(member)
  await page.goto('/account')
  const section = page.getByRole('region', { name: /Household/ })
  await expect(section.getByText('Only a household admin can change these.')).toBeVisible()
  await expect(section.getByRole('switch', { name: 'Watches' })).toBeChecked()
  await expect(section.getByRole('switch', { name: 'Errand autonomy' })).not.toBeChecked()
  for (const s of SWITCHES) await expect(section.getByRole('switch', { name: s.label })).toBeDisabled()

  // The server refuses a member's write too.
  for (const s of SWITCHES) {
    const r = await call('PUT', `${settingsURL(admin.householdId)}/${s.key}`, { token: member.token, body: { value: false } })
    expect(r.status, s.key).toBe(403)
  }
  const after = await must('GET', settingsURL(admin.householdId), { token: member.token })
  expect(after.settings['watches.enabled']).toBe(true)
})
