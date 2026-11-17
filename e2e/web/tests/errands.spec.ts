// Scheduled errands on the web (mobile's Schedules screen): an errand scheduled in chat
// (scripted model, e2e/llm/web.json) is listed with its cadence, and Stop cancels it on the server.
import { must, newUser, provisionNode, unique, useScript } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test.beforeAll(() => useScript('e2e/llm/web.json'))

test('an errand scheduled in chat is listed, and Stop cancels it', async ({ asUser }) => {
  const u = await newUser('errands')
  await provisionNode(u.token, u.householdId, { id: unique('kitchen'), name: 'Kitchen', room: 'kitchen' })
  const page = await asUser(u)
  await page.goto('/errands')
  await expect(page.getByText('No scheduled errands')).toBeVisible()

  await sidebar(page).getByRole('link', { name: 'Chat' }).click()
  await page.getByLabel('Message').fill('every morning at 8, check the e2e weather')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.locator('[data-role="assistant"]').last()).toContainText("Okay — I'll do that every day at 8am")

  await sidebar(page).getByRole('link', { name: 'Errands' }).click()
  const goal = 'Check the e2e weather and say if I need an umbrella'
  await expect(page.getByText(goal)).toBeVisible()
  await expect(page.getByText(/every day/)).toBeVisible()

  await page.getByRole('button', { name: `Stop: ${goal}` }).click()
  const dialog = page.getByRole('dialog', { name: 'Stop this errand?' })
  await expect(dialog).toContainText('It will stop repeating.')
  await dialog.getByRole('button', { name: 'Stop errand' }).click()
  await expect(page.getByText('No scheduled errands')).toBeVisible()
  const left = await must('GET', `${env('SBX_CC')}/api/v0/mobile/household/${u.householdId}/schedules`, { token: u.token })
  expect(left.schedules).toEqual([])
})
