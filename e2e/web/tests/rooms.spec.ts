// Rooms on the web (mobile's Rooms, from the Devices page): a member adds a room and one inside
// it, renames it, and deletes the outer one, which takes the inner one with it (cc deletes a
// room's descendants). Each checked on the server.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import type { Page } from '@playwright/test'
import { expect, test } from '../helpers/fixtures'

// shot keeps the dialog at a phone and a desktop, light and dark, for the PR's audit (as
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

test('rooms are added, nested, renamed and deleted', async ({ asUser }) => {
  const u = await newUser('rooms')
  const rooms = async () =>
    (
      (await must('GET', `${env('SBX_CC')}/api/v0/households/${u.householdId}/rooms`, { token: u.token })) as {
        id: string
        name: string
        parent_room_id: string | null
      }[]
    )
      .map((r) => r.name + (r.parent_room_id ? ' (inside)' : ''))
      .sort()
  const page = await asUser(u)
  await page.goto('/devices')
  await page.getByRole('button', { name: 'Rooms', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Rooms' })

  await dialog.getByRole('button', { name: 'Add room' }).click()
  await dialog.getByLabel('Name').fill('Upstairs')
  await dialog.getByRole('button', { name: 'Add', exact: true }).click()
  await expect.poll(rooms).toEqual(['Upstairs'])

  await dialog.getByRole('button', { name: 'Add room' }).click()
  await dialog.getByLabel('Name').fill('Bedrom')
  await dialog.getByLabel('Inside').selectOption({ label: 'Upstairs' })
  await dialog.getByRole('button', { name: 'Add', exact: true }).click()
  await expect.poll(rooms).toEqual(['Bedrom (inside)', 'Upstairs'])
  await expect(dialog.getByText('In Upstairs · 0 devices')).toBeVisible()

  await dialog.getByRole('button', { name: 'Edit Bedrom' }).click()
  await dialog.getByLabel('Name').fill('Bedroom')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect.poll(rooms).toEqual(['Bedroom (inside)', 'Upstairs'])

  await shot(page, 'list')
  await dialog.getByRole('button', { name: 'Delete Upstairs' }).click()
  const confirm = dialog.getByRole('group', { name: 'Delete Upstairs' })
  await expect(confirm).toContainText('Delete Upstairs and the room inside it (Bedroom)?')
  await shot(page, 'delete')
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect.poll(rooms).toEqual([])
})
