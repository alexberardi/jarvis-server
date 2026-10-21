// Node pairing without hardware: a simulated node (the real agent's enrollment, headless) asks
// to pair, the superuser approves its code on the Nodes page, and the node joins and comes
// online. The app's approval is jarvis-node-mobile's Maestro flow (docs/dev/e2e.md).
import { stateFile } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { startSimNode } from '../helpers/simnode'

test.use({ storageState: stateFile('superuser') })

test('a node pairs with a code approved on the Nodes page and comes online', async ({ page }) => {
  const name = `sim-${Date.now().toString(36)}`
  const room = `room-${Date.now().toString(36)}`
  const code = await startSimNode(name)

  await page.goto('/admin/nodes')
  const pending = page.locator('section', { has: page.getByRole('heading', { name: 'Pending pairings' }) })
  await expect(pending.getByTestId('pairings-waiting')).toBeVisible()
  await pending.getByLabel('Code shown on the computer').fill(code)
  await pending.getByLabel('Room').fill(room)
  await pending.getByRole('button', { name: 'Approve' }).click()
  await expect(page.getByText(/Approved\. The computer asks to confirm/)).toBeVisible()

  // The agent says yes, registers and connects: its card shows up, online, in the room chosen.
  // The node list doesn't refetch after an approval, so reload until it is there.
  const card = page.locator('[data-testid^="node-"]', { has: page.getByText(name, { exact: true }) }).last()
  await expect(async () => {
    await page.reload()
    await expect(card.getByText('Online')).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 30_000 })
  await expect(card).toContainText(`Room: ${room}`)
})
