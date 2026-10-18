// /admin/connections: the listeners and services the server reports, and an app client's life:
// created here, its key (shown once) works against the API, a rotation replaces it, a revoke
// stops it.
import type { Page } from '@playwright/test'
import { call, must, tokenOf, unique } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

/** ping is an app-authenticated call (jarvis-auth's /internal/app-ping) with the given key. */
const ping = (appId: string, key: string) =>
  call('GET', `${env('SBX_AUTH')}/internal/app-ping`, { headers: { 'X-Jarvis-App-Id': appId, 'X-Jarvis-App-Key': key } })

/** shownKey reads the key out of the show-once dialog and closes it. */
async function shownKey(page: Page, appId: string): Promise<string> {
  const dialog = page.getByRole('dialog', { name: `Key for ${appId}` })
  await expect(dialog).toBeVisible()
  const key = (await dialog.getByTestId('app-key').textContent())!.trim()
  expect(key.length).toBeGreaterThan(16)
  await dialog.getByRole('button', { name: "I've stored it" }).click()
  await expect(dialog).toHaveCount(0)
  return key
}

test.describe('admin connections', () => {
  test("lists jarvisd's listeners and the external services, with their health", async ({ page }) => {
    const c = await must('GET', `${env('SBX_ADMIN')}/api/connections`, { token: tokenOf('superuser') })
    await page.goto('/admin/connections')
    expect(c.listeners.length).toBeGreaterThan(0)
    for (const l of c.listeners) {
      const row = page.getByTestId(`listener-${l.name}`)
      await expect(row).toContainText(l.url)
      await expect(row).toContainText(l.health?.healthy ? 'healthy' : 'unreachable')
    }
    // External services as registered; the sandbox cannot reach the internet ones.
    for (const x of c.external) {
      await expect(page.getByTestId(`external-${x.name}`)).toContainText(x.health?.healthy ? 'healthy' : 'unreachable')
    }
  })

  test('an app client: created, its key works, rotated, revoked', async ({ page }) => {
    const appId = unique('e2e-conn')
    page.on('dialog', (d) => void d.accept()) // the rotate and revoke confirms
    await page.goto('/admin/connections')
    const apps = page.getByRole('region', { name: 'App clients' })
    await apps.getByRole('button', { name: 'New app' }).click()
    await apps.getByLabel('App ID').fill(appId)
    await apps.getByLabel('App name').fill('E2E connections test')
    await apps.getByRole('button', { name: 'Create' }).click()
    const key = await shownKey(page, appId)

    const row = page.getByTestId(`app-${appId}`)
    await expect(row).toContainText('active')
    expect((await ping(appId, key)).status).toBe(200)
    expect((await ping(appId, 'not-the-key')).status).toBe(401)

    await row.getByRole('button', { name: 'Rotate key' }).click()
    const rotated = await shownKey(page, appId)
    expect(rotated).not.toBe(key)
    expect((await ping(appId, key)).status).toBe(401)
    expect((await ping(appId, rotated)).status).toBe(200)

    await row.getByRole('button', { name: 'Revoke' }).click()
    await expect(page.getByText(`Revoked ${appId}`)).toBeVisible()
    await expect(row).toContainText('revoked')
    await expect(row.getByRole('button', { name: 'Reissue key' })).toBeVisible()
    expect((await ping(appId, rotated)).status).toBe(401)
    const listed = (await must('GET', `${env('SBX_ADMIN')}/api/connections`, { token: tokenOf('superuser') })).apps
    expect(listed.find((a: { app_id: string }) => a.app_id === appId)).toMatchObject({ is_active: false })
  })
})
