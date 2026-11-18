// The household's voice and place on the web (mobile's Voice, Location and Time zone): an admin
// loads a voice starter and saves it, saves the town, and picks a time zone; each checked on the
// server, and Automatic puts the zone back.
import { must, newUser } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

test('voice, town and time zone are saved', async ({ asUser }) => {
  const u = await newUser('hh-place')
  const settings = async () => (await must('GET', `${env('SBX_CC')}/api/v0/mobile/household/${u.householdId}/settings`, { token: u.token })).settings
  const page = await asUser(u)
  await page.goto('/household')

  const voice = page.getByRole('region', { name: 'How Jarvis talks' })
  const starters = voice.getByRole('group', { name: 'Starters' })
  const pick = starters.getByRole('button').filter({ hasNotText: '(default)' }).first()
  await pick.click()
  await expect(pick).toHaveAttribute('aria-pressed', 'true')
  const text = await voice.getByLabel('How Jarvis should sound').inputValue()
  await voice.getByRole('button', { name: 'Save' }).click()
  await expect.poll(async () => (await settings())['persona.household_prompt']).toBe(text.trim())

  const place = page.getByRole('region', { name: 'Where you are' })
  await place.getByLabel('Town').fill('Springfield, IL')
  await place.getByRole('button', { name: 'Save' }).click()
  await expect.poll(async () => (await settings())['household.location']).toBe('Springfield, IL')

  await place.getByLabel('Time zone').selectOption('Europe/Paris')
  await expect.poll(async () => (await settings())['household.timezone']).toBe('Europe/Paris')
  await place.getByLabel('Time zone').selectOption('')
  await expect.poll(async () => (await settings())['household.timezone']).toBe('')
})
