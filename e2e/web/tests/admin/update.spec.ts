// /admin/update: the state the server reports, and the opt-in switch saved on the server. It
// never applies an update: /api/update/apply is blocked for the whole test.
import { must, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const update = () => must('GET', `${env('SBX_ADMIN')}/api/update`, { token: tokenOf('superuser') })
const setEnabled = (enabled: boolean) =>
  must('PUT', `${env('SBX_ADMIN')}/api/update/settings`, { token: tokenOf('superuser'), body: { enabled } })

test.describe('admin update', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/update/apply', (r) => (r.request().method() === 'GET' ? r.continue() : r.abort()))
  })

  test('with checks off: says so, names the running version, offers no update', async ({ page }) => {
    await setEnabled(false)
    const info = await update()
    expect(info).toMatchObject({ updates_enabled: false, update_available: false, can_apply: false })
    await page.goto('/admin/update')
    await expect(page.getByTestId('verdict')).toContainText('Update checks are off')
    await expect(page.getByText(`Running ${info.current_version} on ${info.platform}.`)).toBeVisible()
    await expect(page.getByRole('switch', { name: 'Check for updates' })).not.toBeChecked()
    await expect(page.getByRole('button', { name: 'Check now' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Update now' })).toHaveCount(0)
  })

  test('turning checks on is saved on the server, and off again', async ({ page }) => {
    await setEnabled(false)
    try {
      await page.goto('/admin/update')
      const toggle = page.getByRole('switch', { name: 'Check for updates' })
      await toggle.click()
      await expect(toggle).toBeChecked()
      await expect(page.getByRole('button', { name: 'Check now' })).toBeVisible()
      await expect.poll(async () => (await update()).updates_enabled).toBe(true)
      // The check itself asks GitHub; whatever it finds, a dev build is never offered an update.
      await expect(page.getByTestId('verdict')).not.toContainText('Update checks are off')
      await expect(page.getByRole('button', { name: 'Update now' })).toHaveCount(0)

      await page.reload()
      await expect(page.getByRole('switch', { name: 'Check for updates' })).toBeChecked()
      await page.getByRole('switch', { name: 'Check for updates' }).click()
      await expect(page.getByRole('switch', { name: 'Check for updates' })).not.toBeChecked()
      await expect(page.getByTestId('verdict')).toContainText('Update checks are off')
      await expect.poll(async () => (await update()).updates_enabled).toBe(false)
    } finally {
      await setEnabled(false)
    }
  })

  test('an available update, and applying it', async () => {
    test.skip(true, 'an "update available" state needs jarvisd restarted onto a fake release server (scripts/upgrade-helper-e2e.sh does that for the updater); applying one is never run from this suite')
  })
})
