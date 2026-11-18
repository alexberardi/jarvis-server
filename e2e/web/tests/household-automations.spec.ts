// Signal automations on the web (mobile's Automations): an admin writes what Jarvis does when
// they leave home, chooses "Just do it", and saves; then clears it. Each checked on the server.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

test('an automation is saved and cleared', async ({ asUser }) => {
  const u = await newUser('hh-auto')
  const rule = async () =>
    (
      (await must('GET', `${env('SBX_CC')}/api/v0/mobile/household/${u.householdId}/signal-automations`, { token: u.token })).automations as {
        kind: string
        instruction: string
        enabled: boolean
        delivery: string
      }[]
    ).find((a) => a.kind === 'presence.left')
  const page = await asUser(u)
  await page.goto('/household')

  const card = page.getByRole('region', { name: 'When something happens' })
  const left = card.getByRole('form', { name: 'I leave home' })
  await left.getByLabel('What Jarvis should do when i leave home').fill('Lock the front door')
  await expect(left.getByRole('switch', { name: 'On' })).toBeChecked()
  await left.getByText('Just do it').click()
  await left.getByRole('button', { name: 'Save' }).click()
  await expect.poll(rule).toMatchObject({ instruction: 'Lock the front door', enabled: true, delivery: 'automatic' })
  await expect(left.getByRole('button', { name: 'Save' })).toBeDisabled()

  await left.getByLabel('What Jarvis should do when i leave home').fill('')
  await left.getByRole('button', { name: 'Clear' }).click()
  await expect.poll(async () => (await rule())?.instruction).toBe('')
})
