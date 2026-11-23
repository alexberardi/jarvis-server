// Recent commands on the web (mobile's Recent Commands): a chat turn shows up with what was said
// and the reply. cc records only commands a node ran, so this server-side remember shows the
// reply instead (the call lines are covered by RecentCommandsPage.test.tsx).
import { newUser, provisionNode, unique, useScript } from '../helpers/api'
import { expect, sidebar, test } from '../helpers/fixtures'

test.beforeAll(() => useScript('e2e/llm/web.json'))

test('a chat turn is listed with its reply', async ({ asUser }) => {
  const u = await newUser('recent')
  await provisionNode(u.token, u.householdId, { id: unique('kitchen'), name: 'Kitchen', room: 'kitchen' })
  const page = await asUser(u)
  await page.goto('/recent')
  await expect(page.getByText('Nothing yet')).toBeVisible()

  await sidebar(page).getByRole('link', { name: 'Chat' }).click()
  await page.getByLabel('Message').fill('remember that my e2e code word is marmalade')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.locator('[data-role="assistant"]').last()).toContainText('Saved to your memories.')

  await sidebar(page).getByRole('link', { name: 'Recent commands' }).click()
  const row = page.getByRole('list', { name: 'Recent commands' }).getByRole('listitem').first()
  await expect(row).toContainText('“remember that my e2e code word is marmalade”')
  await expect(row).toContainText('Saved to your memories.')
  await row.getByText('“remember that my e2e code word is marmalade”').click()
  await expect(row.getByText('Jarvis said')).toBeVisible()
})
