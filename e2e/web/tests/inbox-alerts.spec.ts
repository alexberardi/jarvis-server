// The inbox's blocked suggestions and live alerts (docs/webapp/03 steps 4 and 5): a suggestion
// blocked from a card shows up and can be allowed again; a new item toasts with Open.
import { createInboxItem, must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

const cc = () => env('SBX_CC')

test('a blocked suggestion is listed and can be allowed again', async ({ asUser }) => {
  const u = await newUser('blocked')
  // What "Never suggest this" on a proposal card sends.
  const job = await must('POST', `${cc()}/api/v0/callbacks`, {
    token: u.token,
    body: {
      command_name: 'jarvis.proposable_action',
      callback_name: 'suppress',
      household_id: u.householdId,
      navigation_type: 'new_notification',
      data: { _action: { target_command: 'send_message', idempotency_key: 'e2e-blocked' }, source: 'sam', descriptor: 'Text Sam good morning' },
    },
  })
  await expect
    .poll(async () => (await must('GET', `${cc()}/api/v0/callbacks/${job.id}/status`, { token: u.token })).status)
    .toBe('completed')

  const page = await asUser(u)
  await page.goto('/inbox')
  await page.getByRole('link', { name: 'Blocked suggestions' }).click()
  await expect(page.getByRole('heading', { name: 'Blocked suggestions' })).toBeVisible()
  await expect(page.getByText('Text Sam good morning')).toBeVisible()

  await page.getByRole('button', { name: 'Allow again: Text Sam good morning' }).click()
  await expect(page.getByText('Nothing blocked')).toBeVisible()
  const left = await must('GET', `${cc()}/api/v0/mobile/proposal-suppressions?household_id=${u.householdId}`, { token: u.token })
  expect(left.suppressions).toEqual([])
})

test('a new item raises the badge and toasts, and Open goes to it', async ({ asUser }) => {
  const u = await newUser('alerts')
  await createInboxItem(u, { title: 'Already here' })
  const page = await asUser(u)
  await page.goto('/chat')
  await expect(sidebar(page).getByLabel('1 unread')).toBeVisible()
  // Items already there are the baseline: no toast for them.
  await expect(page.getByText('Already here')).toHaveCount(0)

  const item = await createInboxItem(u, { title: 'Your research is ready', summary: 'Best coffee beans' })
  // Coming back to the tab refetches the count now instead of at the next poll.
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange', { bubbles: true })))
  await expect(sidebar(page).getByLabel('2 unread')).toBeVisible()
  const toast = page.locator('[data-sonner-toast]', { hasText: 'Your research is ready' })
  await expect(toast).toBeVisible()
  await toast.getByRole('button', { name: 'Open' }).click()
  await expect(page).toHaveURL(new RegExp(`/inbox/${item.id}$`))
})
