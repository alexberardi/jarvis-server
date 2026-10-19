// Workflows and Recordings (computer control, plan P3 piece 27): the rich profile seeds the
// superuser's bank download (and its recordings) and the partner's shared bill payment
// (scripts/seed-dev.py). The list shows them, an edit is saved on the server, a recording is
// viewed and deleted, and a household admin changes how long recordings are kept.
import { must, tokenOf } from '../helpers/api'
import { env, stateFile } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const BANK = 'tw_5eed000000000001'
const cc = (p: string) => `${env('SBX_CC')}/api/v0${p}`

test('lists the taught workflows: yours and the shared one', async ({ page }) => {
  await page.goto('/chat')
  await sidebar(page).getByRole('link', { name: 'Workflows' }).click()
  await expect(page.getByRole('heading', { name: 'Workflows', exact: true })).toBeVisible()
  const bank = page.getByRole('link', { name: /Download bank statement/ })
  await expect(bank).toContainText('Yours')
  await expect(bank).toContainText('5 steps')
  const water = page.getByRole('link', { name: /Pay the water bill/ })
  await expect(water).toContainText('Another member’s')
  await expect(water).toContainText('Shared with the household')

  // Replay arrives in a later piece: the server answers 501 and the page says so.
  await page.getByRole('button', { name: 'Run Download bank statement' }).click()
  await expect(page.getByText("Running a workflow from here isn't available yet.")).toBeVisible()
})

test('edits a workflow and the server keeps the edit', async ({ page }) => {
  const token = tokenOf('superuser')
  const before = await must('GET', cc(`/workflows/${BANK}`), { token })
  try {
    await page.goto('/workflows')
    await page.getByRole('link', { name: /Download bank statement/ }).click()
    await expect(page).toHaveURL(new RegExp(`/workflows/${BANK}$`))
    const steps = page.getByTestId('step')
    await expect(steps).toHaveCount(5)
    await expect(steps.nth(0)).toContainText('Open bank.example.com')
    await expect(steps.nth(1).locator('strong')).toHaveText('Statements')
    await expect(steps.nth(2)).toContainText('Type {month}')

    const params = page.getByRole('region', { name: 'Which of these change each time?' })
    await params.getByRole('switch', { name: 'checking changes each time' }).check()
    await params.getByLabel('Name for checking').fill('account')
    await expect(steps.nth(3)).toContainText('Choose {account}')
    await page.getByRole('button', { name: 'Move step 5 up' }).click()
    await page.getByRole('switch', { name: 'Ask first before step 1' }).check()
    await page.getByRole('switch', { name: 'Share with the household' }).check()
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Saved')).toBeVisible()

    const after = await must('GET', cc(`/workflows/${BANK}`), { token })
    expect(after.shared).toBe(true)
    expect(after.params.map((p: { name: string }) => p.name)).toEqual(['month', 'account'])
    expect(after.steps.map((s: { intent: string }) => s.intent)).toEqual([
      'Open the bank',
      'Go to statements',
      'Enter the month {month}',
      'Download the CSV',
      'Pick the {account} account',
    ])
    expect(after.steps[4].action.text).toBe('{account}')
    expect(after.steps[0].risk).toBe('submit_third_party')

    // What the page shows after a reload came from the server.
    await page.reload()
    await expect(page.getByTestId('step').nth(4)).toContainText('Choose {account}')
    await expect(page.getByTestId('step').nth(0)).toContainText('Asks first')
  } finally {
    const { name, triggers, params, steps, success, shared } = before
    await must('PUT', cc(`/workflows/${BANK}`), { token, body: { name, triggers, params, steps, success, shared } })
  }
})

test('views and deletes a recording', async ({ page }) => {
  const token = tokenOf('superuser')
  await page.goto('/workflows')
  await page.getByRole('link', { name: 'Recordings' }).first().click()
  await expect(page.getByRole('heading', { name: 'Recordings', exact: true })).toBeVisible()
  const items = page.getByTestId('recording')
  await expect(items).toHaveCount(2)
  await expect(items.nth(0)).toContainText('7 steps')
  await expect(items.nth(0)).toContainText('Deleted in')

  await items.nth(0).getByRole('button', { expanded: false }).click()
  await expect(items.nth(0).getByText('Opened bank.example.com')).toBeVisible()
  // A masked field shows no value.
  await expect(items.nth(0).getByText(/Typed \(hidden\) in/)).toBeVisible()
  await expect(items.nth(0).getByText('3 keyframes, kept only on the computer that recorded it.')).toBeVisible()

  page.once('dialog', (d) => void d.accept())
  await items.nth(1).getByRole('button', { name: /^Delete Recording from/ }).click()
  await expect(page.getByText('Recording deleted')).toBeVisible()
  await expect(items).toHaveCount(1)
  const hh = (await must('GET', cc(`/workflows/${BANK}`), { token })).household_id
  const left = await must('GET', cc(`/recordings?household_id=${hh}`), { token })
  expect(left.recordings.map((r: { id: string }) => r.id)).toEqual(['rec_5eed000000000001'])
})

test('a household admin changes how long recordings are kept', async ({ page }) => {
  const token = tokenOf('superuser')
  const hh = (await must('GET', cc(`/workflows/${BANK}`), { token })).household_id
  const url = `${env('SBX_CC')}/api/v0/mobile/household/${hh}/settings`
  try {
    await page.goto('/recordings')
    const section = page.getByRole('region', { name: 'How long recordings are kept' })
    const days = section.getByLabel('Keep for')
    await expect(days).toHaveValue('30')
    await days.fill('14')
    await section.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Saved')).toBeVisible()
    await expect
      .poll(async () => (await must('GET', url, { token })).settings['computer.recording_retention_days'])
      .toBe(14)
  } finally {
    await must('PUT', `${url}/computer.recording_retention_days`, { token, body: { value: 30 } })
  }
})
