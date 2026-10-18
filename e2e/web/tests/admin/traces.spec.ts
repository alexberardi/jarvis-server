// /admin/traces and a trace's detail: a chat turn the test sends (scripted model) shows up as a
// trace, on the dashboard's recent requests too, and its detail is that turn.
import type { Page } from '@playwright/test'
import { must, tokenOf, unique, useScript } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })
test.beforeAll(() => useScript('e2e/llm/web.json'))

async function chat(page: Page, text: string) {
  await page.goto('/chat')
  await page.getByLabel('Message').fill(text)
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.locator('[data-role="assistant"]').last()).toHaveText('Hello from the scripted model.')
}

test.describe('admin traces', () => {
  test("a chat turn is traced: listed, on the dashboard, and its detail is the turn's", async ({ page }) => {
    const said = `hello, trace ${unique('t')}`
    await chat(page, said)
    // The server recorded it.
    await expect
      .poll(async () => {
        const b = await must('GET', `${env('SBX_ADMIN')}/api/traces?limit=20`, { token: tokenOf('superuser') })
        return b.traces.find((t: { user_command?: string }) => t.user_command === said)?.id ?? null
      })
      .not.toBeNull()

    await page.goto('/admin/dashboard')
    await expect(page.getByRole('region', { name: 'Recent requests' }).getByRole('link', { name: new RegExp(said) })).toBeVisible()

    await page.goto('/admin/traces')
    const rows = page.locator('tbody tr', { hasText: said })
    await expect(rows.first()).toContainText('Hello from the scripted model.')
    await rows.first().click() // opens the conversation's traces
    await rows.last().click()
    await expect(page).toHaveURL(/\/admin\/traces\/[^/]+$/)
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(said)
    await expect(page.getByRole('heading', { name: 'Service Flow' })).toBeVisible()

    await page.getByRole('button', { name: 'Back to traces' }).click()
    await expect(page).toHaveURL(/\/admin\/traces$/)
  })
})
