import { stateFile } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { startSimNode } from '../helpers/simnode'

test.use({ storageState: stateFile('superuser') })

for (const profile of ['pi', 'desktop'] as const) {
test(`node settings save and reload without K2 (${profile})`, async ({ page, consoleErrors }, info) => {
  const name = `settings-${Date.now().toString(36)}`
  const code = await startSimNode(name, profile)
  await page.goto('/admin/nodes')
  await page.getByLabel('Code shown on the computer').fill(code)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  const card = page.locator('[data-testid^="node-"]', { has: page.getByText(name, { exact: true }) }).last()
  await expect(async () => {
    await page.reload()
    await expect(card.getByText('Online')).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 30_000 })
  await card.getByRole('button', { name: 'Settings' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Follow-up duration')).toHaveValue('3')
  await dialog.getByLabel('Follow-up duration').fill('1.5')
  await dialog.getByRole('button', { name: 'Save changes' }).click()
  await expect(dialog.getByRole('status')).toContainText('Saved on the node')
  await dialog.getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(dialog.getByLabel('Follow-up duration')).toHaveValue('1.5')
  await expect(dialog.getByLabel('LEDs', { exact: true })).toHaveCount(0)
  await expect(dialog.getByLabel('Automatic updates')).toHaveCount(0)
  for (const colorScheme of ['light', 'dark'] as const) {
    for (const width of [390, 820, 1440]) {
      await page.emulateMedia({ colorScheme })
      await page.evaluate(theme => { localStorage.setItem('jarvis-admin-theme', theme); document.documentElement.classList.toggle('dark', theme === 'dark') }, colorScheme)
      await page.setViewportSize({ width, height: 900 })
      const shot = info.outputPath(`node-settings-${width}-${colorScheme}.png`)
      await page.screenshot({ path: shot, animations: 'disabled' })
      await info.attach('screenshot', { path: shot, contentType: 'image/png' })
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    }
  }
  expect(consoleErrors).toEqual([])
})
}
