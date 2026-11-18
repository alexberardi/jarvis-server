// Stored data on the web (mobile's Stored data): a member approves a computer's code into their
// household from Nodes, asks Jarvis to add oat milk to the shopping list (the node stores it),
// then finds the record under the node's Stored data, checks it off and saves; the node has it.
import { must, newUser, useScript } from '../helpers/api'
import { env } from '../helpers/env'
import type { Page } from '@playwright/test'
import { expect, sidebar, test } from '../helpers/fixtures'
import { startSimNode } from '../helpers/simnode'

test.beforeAll(() => useScript('e2e/llm/web.json'))

// shot keeps the page at a phone and a desktop, light and dark, for the PR's audit (as
// devices.spec.ts does), and checks nothing scrolls sideways.
async function shot(page: Page, state: string) {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const [viewport, width, height] of [
    ['phone', 390, 844],
    ['desktop', 1440, 900],
  ] as const) {
    await page.setViewportSize({ width, height })
    for (const theme of ['light', 'dark']) {
      await page.evaluate((dark) => document.documentElement.classList.toggle('dark', dark), theme === 'dark')
      // Theme colors use the shared 120ms transition; capture the settled palette.
      await page.waitForTimeout(200)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await page.screenshot({ path: test.info().outputPath(`${state}-${viewport}-${theme}.png`) })
    }
  }
  await page.evaluate(() => document.documentElement.classList.remove('dark'))
}

test('a node’s record is found and edited', async ({ asUser }) => {
  const u = await newUser('stored')
  const name = `stored-${Date.now().toString(36)}`
  const code = await startSimNode(name)
  const page = await asUser(u)
  await page.goto('/nodes')
  await page.getByLabel('Code shown on the computer').fill(code)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  const card = page.locator('[data-testid^="node-"]', { has: page.getByText(name, { exact: true }) })
  await expect(async () => {
    await page.reload()
    await expect(card.getByText('Online')).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 30_000 })
  const nodeId = (await card.getAttribute('data-testid'))!.replace(/^node-/, '')

  await sidebar(page).getByRole('link', { name: 'Chat' }).click()
  await page.getByLabel('Message').fill('add oat milk to the shopping list')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.locator('[data-role="assistant"]').last()).toContainText('Added oat milk to the shopping list.')

  await sidebar(page).getByRole('link', { name: 'Nodes' }).click()
  await card.getByRole('link', { name: 'Stored data' }).click()
  await page.getByLabel('Command').selectOption({ label: 'Shopping list' })
  const records = page.getByRole('list', { name: 'Records' })
  await expect(records.getByRole('button', { name: /oat milk/i })).toBeVisible()
  await shot(page, 'records')
  await records.getByRole('button', { name: /oat milk/i }).click()
  const dialog = page.getByRole('dialog', { name: 'Shopping list' })
  await expect(dialog.getByText('Item', { exact: true })).toBeVisible()
  await shot(page, 'record')
  await dialog.getByRole('switch', { name: 'Checked off' }).click()
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)

  const stored = async () =>
    (await must('GET', `${env('SBX_CC')}/api/v0/mobile/command-data/nodes/${nodeId}/commands/shopping_list/records`, { token: u.token })).records as {
      data: { item: string; checked?: boolean }
    }[]
  await expect.poll(async () => (await stored()).map((r) => [r.data.item, r.data.checked === true])).toEqual([['oat milk', true]])
})
