// Memories and the phonebook on the web (mobile's Memories and Phonebook screens): add, edit and
// remove one of each through the pages, each change checked on the server.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test('a memory is added, edited and forgotten', async ({ asUser }) => {
  const u = await newUser('memories')
  const memories = async () => (await must('GET', `${env('SBX_CC')}/api/v0/mobile/memories?household_id=${u.householdId}`, { token: u.token })) as any[]
  const page = await asUser(u)
  await page.goto('/chat')
  await sidebar(page).getByRole('link', { name: 'Memories' }).click()
  await expect(page.getByText('Nothing remembered yet')).toBeVisible()

  await page.getByRole('button', { name: 'New memory' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('What should Jarvis remember?').fill('Likes oat milk in coffee')
  await dialog.locator('label', { hasText: 'Preference' }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('region', { name: 'Yours' })).toContainText('Likes oat milk in coffee')
  let saved = (await memories()).find((m) => m.content === 'Likes oat milk in coffee')
  expect(saved).toMatchObject({ category: 'preference', user_id: u.id })

  await page.getByRole('button', { name: 'Edit: Likes oat milk in coffee' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('What should Jarvis remember?').fill('Likes oat milk, no sugar')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Likes oat milk, no sugar')).toBeVisible()
  saved = (await memories()).find((m) => m.id === saved.id)
  expect(saved.content).toBe('Likes oat milk, no sugar')

  await page.getByRole('button', { name: 'Forget: Likes oat milk, no sugar' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Forget' }).click()
  await expect(page.getByText('Nothing remembered yet')).toBeVisible()
  expect((await memories()).some((m) => m.id === saved.id)).toBe(false)
})

test('a business is added, marked do-not-call and removed', async ({ asUser }) => {
  const u = await newUser('phonebook')
  const contacts = async () => (await must('GET', `${env('SBX_CC')}/api/v0/mobile/household/${u.householdId}/phone-contacts`, { token: u.token })).contacts as any[]
  const page = await asUser(u)
  await page.goto('/chat')
  await sidebar(page).getByRole('link', { name: 'Phonebook' }).click()
  await expect(page.getByText('No businesses yet')).toBeVisible()

  await page.getByRole('button', { name: 'Add business' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill('E2E Pizzeria')
  await dialog.getByLabel('Phone number').fill('+1 212 555 0123')
  await dialog.getByRole('button', { name: 'Save' }).click()
  const row = page.getByRole('list', { name: 'Businesses' }).getByRole('listitem').filter({ hasText: 'E2E Pizzeria' })
  await expect(row).toContainText('(212) 555-0123')
  const [made] = await contacts()
  expect(made).toMatchObject({ name: 'E2E Pizzeria', do_not_call: false })

  await row.getByRole('button', { name: 'Edit E2E Pizzeria' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('switch', { name: /Do not call/ }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(row).toContainText('Do not call')
  expect((await contacts())[0].do_not_call).toBe(true)

  await row.getByRole('button', { name: 'Remove E2E Pizzeria' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect(page.getByText('No businesses yet')).toBeVisible()
  expect(await contacts()).toEqual([])
})
