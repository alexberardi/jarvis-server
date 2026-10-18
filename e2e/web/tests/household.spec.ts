// The household switcher: shown with two households, and what Chat and the inbox show
// follows the one picked.
import { createHousehold, createInboxItem, newUser, provisionNode, switchHousehold } from '../helpers/api'
import { expect, test } from '../helpers/fixtures'

test('switching household switches the inbox and the chat devices', async ({ asUser }) => {
  const u = await newUser('hh', 'Home One')
  const second = await createHousehold(u.token, 'Home Two')
  const t2 = await switchHousehold(u.token, second)
  await createInboxItem(u, { title: 'Only in Home One' })
  await createInboxItem({ id: u.id, householdId: second }, { title: 'Only in Home Two' })
  await provisionNode(u.token, u.householdId, { id: crypto.randomUUID(), name: 'one', room: 'garage' })
  await provisionNode(t2, second, { id: crypto.randomUUID(), name: 'two', room: 'attic' })

  const page = await asUser(u)
  await page.goto('/inbox')
  const switcher = page.getByRole('banner').getByLabel('Household')
  await expect(switcher).toBeVisible()
  await expect(switcher.locator('option')).toHaveText(['Home One', 'Home Two'])

  await switcher.selectOption({ label: 'Home One' })
  await expect(page.locator('main li')).toHaveText([/Only in Home One/])
  await page.goto('/chat')
  await expect(page.getByLabel('Device').locator('option')).toHaveText([/garage/])

  await page.getByRole('banner').getByLabel('Household').selectOption({ label: 'Home Two' })
  await expect(page.getByLabel('Device').locator('option')).toHaveText([/attic/])
  await page.goto('/inbox')
  await expect(page.locator('main li')).toHaveText([/Only in Home Two/])

  // The choice survives a reload.
  await page.reload()
  await expect(page.getByRole('banner').getByLabel('Household')).toHaveValue(second)
  await expect(page.locator('main li')).toHaveText([/Only in Home Two/])
})

test('one household: no switcher', async ({ asUser }) => {
  const u = await newUser('hh1')
  const page = await asUser(u)
  await page.goto('/inbox')
  await expect(page.getByLabel('Household')).toHaveCount(0)
})
