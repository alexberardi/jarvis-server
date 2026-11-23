// Now playing on the web (mobile's Now playing): a node reports a track and the page shows it
// with only the controls its source lists; a control to a node that isn't connected says so.
import { must, newUser, provisionNode, unique } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

test('what a node plays shows up, with its controls', async ({ asUser }) => {
  const u = await newUser('playing')
  const id = unique('speaker')
  const key = await provisionNode(u.token, u.householdId, { id, name: 'Kitchen speaker', room: 'kitchen' })
  await must('POST', `${env('SBX_CC')}/api/v0/node/playback`, {
    headers: { 'X-API-Key': `${id}:${key}` },
    body: {
      now: {
        source: 'radio',
        name: 'Radio',
        updated_at: new Date().toISOString(),
        state: {
          state: 'playing',
          media_title: 'Morning Edition',
          media_artist: 'WBEZ',
          media_album: '',
          context: 'WBEZ 91.5',
          actions: ['pause', 'volume'],
          volume: 40,
        },
      },
    },
  })
  const page = await asUser(u)
  await page.goto('/now-playing')

  const card = page.getByRole('region', { name: 'Kitchen speaker: Morning Edition' })
  await expect(card.getByText('Kitchen speaker · WBEZ 91.5')).toBeVisible()
  await expect(card.getByText('WBEZ', { exact: true })).toBeVisible()
  await expect(card.getByLabel('Volume')).toHaveValue('40')
  // The source lists no next or previous, so there are no such buttons.
  await expect(card.getByRole('button', { name: 'Next track' })).toHaveCount(0)
  await card.getByRole('button', { name: 'Pause' }).click()
  await expect(page.getByRole('alert')).toContainText('offline')
})
