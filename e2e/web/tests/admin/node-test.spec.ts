// /admin/nodes/:id/test: a node card links to its Forge test page, which takes a share code and
// asks cc for the test install on that node. The sandbox has no Pantry, so cc refuses with the
// household's Pantry switch (off by default), and the page shows why; the install, the chat and
// Send to Forge against a real Pantry are checked by hand (docs/decisions/2026-11-09-web-forge-test.md).
import { stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'
import { startSimNode } from '../../helpers/simnode'

test.use({ storageState: stateFile('superuser') })

test('a node card opens its Forge test page, which asks cc for the test install', async ({ page }) => {
  const name = `forge-${Date.now().toString(36)}`
  const code = await startSimNode(name, 'desktop')

  await page.goto('/admin/nodes')
  const pending = page.locator('section', { has: page.getByRole('heading', { name: 'Pending pairings' }) })
  await pending.getByLabel('Code shown on the computer').fill(code)
  await pending.getByLabel('Room').fill('desk')
  await pending.getByRole('button', { name: 'Approve' }).click()
  await expect(page.getByText(/Approved\. The computer asks to confirm/)).toBeVisible()

  const card = page.locator('[data-testid^="node-"]', { has: page.getByText(name, { exact: true }) }).last()
  await expect(async () => {
    await page.reload()
    await expect(card.getByText('Online')).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 30_000 })

  await card.getByRole('link', { name: 'Test a Forge command' }).click()
  await expect(page).toHaveURL(/\/admin\/nodes\/[^/]+\/test$/)
  await expect(page.getByRole('heading', { name: 'Test a Forge command' })).toBeVisible()

  const install = page.getByRole('button', { name: `Install on ${name}` })
  await page.getByLabel('Share code').fill('ab3km7')
  await expect(page.getByLabel('Share code')).toHaveValue('AB3KM7')
  await install.click()
  await expect(page.getByText('The Pantry package store is turned off for this household.', { exact: false })).toBeVisible()
  await expect(page).toHaveURL(/\/test$/)
})
