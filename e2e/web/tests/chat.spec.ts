// Chat through a node with the scripted model (e2e/llm/web.json): the reply shown, a tool
// call's effect on the server, a photo reaching the model, and the device picker.
import { asSuperuser, memories, modelCalls, setSetting, useScript } from '../helpers/api'
import { env, stateFile } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'

// The seeded nodes (scripts/seed-dev.py): Kitchen is the primary, Living Room the other.
const KITCHEN = '5eed0000-0000-4000-8000-00000000a0de'
const LIVING = '5eed0000-0000-4000-8000-00000000117e'

// An 8x8 red PNG.
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAIAQMAAAD+wSzIAAAABlBMVEX/AAD///9BHTQRAAAAEUlEQVQI12P4z8DAwMDAAAMAD+IB/y/7oK0AAAAASUVORK5CYII=',
  'base64',
)

test.use({ storageState: stateFile('superuser') })
test.beforeAll(() => useScript('e2e/llm/web.json'))

const assistant = (page: import('@playwright/test').Page) => page.locator('[data-role="assistant"]')

async function send(page: import('@playwright/test').Page, text: string) {
  const req = page.waitForRequest((r) => r.url().endsWith('/mobile/chat') && r.method() === 'POST')
  await page.getByLabel('Message').fill(text)
  await page.getByRole('button', { name: 'Send' }).click()
  return (await req).postDataJSON()
}

test('a message gets the scripted reply', async ({ page }) => {
  await page.goto('/chat')
  await send(page, 'hello there')
  await expect(page.locator('[data-role="user"]').last()).toContainText('hello there')
  await expect(assistant(page).last()).toHaveText('Hello from the scripted model.')

  // New conversation clears the thread.
  await page.getByRole('button', { name: 'New conversation' }).click()
  await expect(assistant(page)).toHaveCount(0)
})

test('a tool call does what it says: remember saves a memory', async ({ page }) => {
  const token = await asSuperuser()
  const before = new Set((await memories(token)).map((m) => m.id))
  await page.goto('/chat')
  await send(page, 'remember that my e2e code word is marmalade')
  await expect(assistant(page).last()).toContainText('Saved to your memories.')
  const added = (await memories(token)).filter((m) => !before.has(m.id))
  expect(added.map((m) => m.content)).toEqual(['The e2e code word is marmalade'])
})

test('picking a device sends the turn through it, and the choice is remembered', async ({ page }) => {
  await page.goto('/chat')
  const picker = page.getByLabel('Device')
  await expect(picker.locator('option')).toHaveCount(2)
  await picker.selectOption(LIVING)
  let body = await send(page, 'hello from the living room')
  expect(body.node_id).toBe(LIVING)
  await expect(assistant(page).last()).toHaveText('Hello from the scripted model.')

  await page.reload()
  await expect(page.getByLabel('Device')).toHaveValue(LIVING)
  await page.getByLabel('Device').selectOption(KITCHEN)
  body = await send(page, 'hello from the kitchen')
  expect(body.node_id).toBe(KITCHEN)
  await expect(assistant(page).last()).toHaveText('Hello from the scripted model.')
})

test.describe('with a vision model', () => {
  test.beforeAll(() => setSetting(env('SBX_LLM'), 'llm.live.remote_vision', true))
  test.afterAll(() => setSetting(env('SBX_LLM'), 'llm.live.remote_vision', false))

  test('an attached photo is shown and reaches the model', async ({ page }) => {
    const callsBefore = modelCalls().length
    await page.goto('/chat')
    await page.getByTestId('chat-file').setInputFiles({ name: 'red.png', mimeType: 'image/png', buffer: PNG })
    await expect(page.getByRole('list', { name: 'Photos to send' }).locator('img')).toHaveCount(1)
    const body = await send(page, 'what is in this photo?')
    expect(body.images).toHaveLength(1)
    await expect(assistant(page).last()).toHaveText('Scripted model: I got your photo.')
    await expect(page.locator('[data-role="user"]').last()).toContainText('1 photo')
    // The model got the picture itself, not only the words.
    const turn = modelCalls()
      .slice(callsBefore)
      .map((c) => JSON.stringify(c))
      .find((c) => c.includes('what is in this photo'))
    expect(turn, 'a model call with the message').toBeTruthy()
    expect(turn).toContain('image_url')
  })

  test('without vision there is no photo button', async ({ page }) => {
    await setSetting(env('SBX_LLM'), 'llm.live.remote_vision', false)
    await page.goto('/chat')
    await expect(page.getByLabel('Message')).toBeEnabled()
    await expect(page.getByRole('button', { name: 'Add a photo' })).toHaveCount(0)
  })
})
