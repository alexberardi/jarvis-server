// A household's people on the web (mobile's Household settings): an admin renames the household
// and makes an invite code; someone else joins with it from their own Household page; the admin
// makes them a power user and removes them. Each change checked on the server.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test('rename, invite, join, change a role and remove', async ({ asUser }) => {
  const owner = await newUser('hh-owner', 'E2E home')
  const guest = await newUser('hh-guest', 'Guest home')
  const auth = env('SBX_AUTH')
  const members = async () => (await must('GET', `${auth}/households/${owner.householdId}/members`, { token: owner.token })) as any[]

  const page = await asUser(owner)
  await page.goto('/chat')
  await sidebar(page).getByRole('link', { name: 'Household' }).click()
  await page.getByLabel('Household name').fill('The E2E House')
  await page.getByRole('region', { name: 'Name' }).getByRole('button', { name: 'Save' }).click()
  await expect.poll(async () => (await must('GET', `${auth}/households/${owner.householdId}`, { token: owner.token })).name).toBe('The E2E House')

  const invites = page.getByRole('region', { name: 'Invites' })
  await invites.getByRole('button', { name: 'New invite code' }).click()
  const code = (await invites.getByRole('status').locator('code').textContent())?.trim() ?? ''
  expect(code).not.toBe('')

  const guestPage = await asUser(guest)
  await guestPage.goto('/household')
  const join = guestPage.getByRole('region', { name: 'Join a household' })
  await join.getByLabel('Invite code').fill(code)
  await join.getByRole('button', { name: 'Join' }).click()
  await expect(guestPage.getByRole('heading', { name: 'Household', exact: true })).toBeVisible()
  await expect(guestPage.getByText(/^The E2E House ·/)).toBeVisible()

  await page.reload()
  const list = page.getByRole('list', { name: 'Members' })
  const guestName = guest.email.split('@')[0].replace(/[^a-z0-9]/g, '')
  await list.getByLabel(`Role of ${guestName}`).selectOption('power_user')
  await expect.poll(async () => (await members()).find((m) => m.user_id === guest.id)?.role).toBe('power_user')

  await list.getByRole('button', { name: `Remove ${guestName}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect(list.getByText(guestName)).toHaveCount(0)
  expect((await members()).some((m) => m.user_id === guest.id)).toBe(false)
})
