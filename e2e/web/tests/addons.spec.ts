// Add-ons: the user's desktop agents, and signing one out.
import { call, newUser, provisionNode, unique } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

test('lists my desktop agents and signs one out', async ({ asUser }) => {
  const u = await newUser('addons')
  const page = await asUser(u)
  await page.goto('/addons')
  await expect(page.getByRole('heading', { name: 'Add-ons' })).toBeVisible()
  for (const s of ['This computer', 'Desktop agent', 'Your devices', 'Browser extension', 'Phone app']) {
    await expect(page.getByRole('heading', { name: s })).toBeVisible()
  }
  await expect(page.getByText('No computers yet')).toBeVisible()

  // A desktop agent pairs with the household; Add-ons lists it.
  const id = crypto.randomUUID()
  const name = unique('Study PC')
  await provisionNode(u.token, u.householdId, { id, name, room: 'office', kind: 'desktop' })
  await page.reload()
  const devices = page.getByRole('list', { name: 'Your devices' })
  await expect(devices.getByText(name)).toBeVisible()

  await devices.getByRole('button', { name: 'Sign out' }).click()
  const confirm = page.getByRole('group', { name: `Sign out ${name}` })
  await expect(confirm).toBeVisible()
  await confirm.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByText('No computers yet')).toBeVisible()
  // Signed out on the server: the node's registration is gone.
  const r = await call('GET', `${env('SBX_CC')}/api/v0/admin/nodes/${id}`, { token: u.token })
  expect(r.status).toBe(404)
})
