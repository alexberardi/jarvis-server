// Call details on the web (mobile's Call details): a member adds a common detail and a custom
// one, edits it, and removes it; each checked on the server.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

test('call details are added, edited and removed', async ({ asUser }) => {
  const u = await newUser('call-details')
  const saved = async () =>
    (
      (await must('GET', `${env('SBX_CC')}/api/v0/mobile/call-context`, { token: u.token })).fields as {
        key: string
        label: string
        value: string
        tier: string
      }[]
    ).map((f) => `${f.label}=${f.value}/${f.tier}`)
  const page = await asUser(u)
  await page.goto('/account')
  await page.getByRole('link', { name: /Call details/ }).click()
  await expect(page.getByRole('heading', { name: 'Call details' })).toBeVisible()

  await page.getByRole('button', { name: 'Add detail' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('Start from a common detail').selectOption({ label: 'Full name' })
  await dialog.getByLabel('Value').fill('Robin Okafor')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect.poll(saved).toEqual(['Full name=Robin Okafor/state'])

  await page.getByRole('button', { name: 'Add detail' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('Rewards number')
  await dialog.getByLabel('Value').fill('9982-4471')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect.poll(saved).toEqual(['Full name=Robin Okafor/state', 'Rewards number=9982-4471/if_asked'])

  await page.getByRole('button', { name: 'Edit Rewards number' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByText('May be said freely').click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect.poll(saved).toEqual(['Full name=Robin Okafor/state', 'Rewards number=9982-4471/state'])

  await page.getByRole('button', { name: 'Remove Full name' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect.poll(saved).toEqual(['Rewards number=9982-4471/state'])
})
