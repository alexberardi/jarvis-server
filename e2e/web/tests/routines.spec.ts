// Routines on the web (mobile's Routines tab): make an Advanced routine with a weekday schedule,
// see it listed, edit it, and delete it; each change checked on the server.
import { must, newUser, provisionNode, unique } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test('a routine is made, listed, edited and deleted', async ({ asUser }) => {
  const u = await newUser('routines')
  const node = unique('kitchen')
  await provisionNode(u.token, u.householdId, { id: node, name: 'Kitchen', room: 'kitchen' })
  const routines = async () => (await must('GET', `${env('SBX_CC')}/api/v0/households/${u.householdId}/routines`, { token: u.token })).routines as any[]
  const page = await asUser(u)
  await page.goto('/chat')
  await sidebar(page).getByRole('link', { name: 'Routines' }).click()
  await expect(page.getByRole('heading', { name: 'Routines' })).toBeVisible()
  const before = (await routines()).length // cc seeds a household's starter routines

  await page.getByRole('link', { name: 'New routine' }).click()
  await page.locator('label', { hasText: 'Advanced' }).click()
  await expect(page.getByLabel('Advanced')).toBeChecked()
  await page.getByLabel('Name', { exact: true }).fill('E2E porch')
  await page.getByLabel('Trigger phrases').fill('e2e porch on')
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  await page.getByRole('button', { name: 'Add step' }).click()
  // The simulated node reports no commands, so the command is typed.
  await page.getByRole('region', { name: 'Step 1' }).getByLabel('Command').fill('get_weather')
  await page.getByRole('switch', { name: /Run on a schedule/ }).click()
  await page.getByLabel('Time', { exact: true }).fill('19:30')
  await page.getByRole('button', { name: 'Weekdays' }).click()
  await page.getByLabel('Run on', { exact: true }).selectOption(node)
  await page.getByRole('button', { name: 'Create routine' }).click()

  const row = page.getByRole('listitem').filter({ hasText: 'E2E porch' })
  await expect(row).toContainText('“e2e porch on”')
  await expect(row).toContainText(/Weekdays · 7:30/)
  let made = (await routines()).find((r) => r.name === 'E2E porch')
  expect(made.steps).toEqual([{ command: 'get_weather', label: 'get_weather', args: [] }])
  expect(made.schedule).toMatchObject({ type: 'cron', cron: '30 19 * * 1,2,3,4,5', target_node_id: node })

  await row.getByRole('link', { name: 'Edit E2E porch' }).click()
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('E2E porch')
  await page.getByLabel('Name', { exact: true }).fill('E2E porch light')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('link', { name: 'E2E porch light', exact: true })).toBeVisible()
  made = (await routines()).find((r) => r.id === made.id)
  expect(made.name).toBe('E2E porch light')

  await page.getByRole('button', { name: 'Delete E2E porch light' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete routine' }).click()
  await expect(page.getByRole('link', { name: 'E2E porch light', exact: true })).toHaveCount(0)
  expect((await routines()).length).toBe(before)
})
