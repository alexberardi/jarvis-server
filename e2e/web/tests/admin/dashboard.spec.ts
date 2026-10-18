// /admin/dashboard: the System card against /api/system/info, the power controls (never
// stopping the shared sandbox), and the banners driven by the state behind them.
import { must, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const api = (path: string) => must('GET', `${env('SBX_ADMIN')}${path}`, { token: tokenOf('superuser') })

// The required setup jobs (web/admin/src/components/setup/jobs.ts) and their banner titles.
const REQUIRED: Record<string, string> = { llm: 'Language model', stt: 'Speech-to-text', voice: 'Voice' }
const UNDER_WAY = ['ready', 'loading', 'downloading']

test.describe('admin dashboard', () => {
  test('the System card shows what the server reports', async ({ page }) => {
    const info = await api('/api/system/info')
    await page.goto('/admin/dashboard')
    const system = page.getByRole('region', { name: 'System' })
    await expect(system.getByText(info.hostname, { exact: true })).toBeVisible()
    await expect(system).toContainText(`${info.platform}/${info.arch} · ${info.cpuCount} cores`)
    const serving = info.listeners.filter((l: { served: boolean }) => l.served).length
    await expect(system).toContainText(`${serving} serving`)
    await system.getByRole('link', { name: 'connections' }).click()
    await expect(page).toHaveURL(/\/admin\/connections$/)
  })

  test('power: no Restart without a supervisor; Stop asks first and Cancel leaves jarvisd running', async ({ page }) => {
    const info = await api('/api/system/info')
    // The sandbox runs unsupervised: restart is refused, stop is offered.
    expect(info.capabilities).toMatchObject({ restart: false, stop: true })
    // Belt and braces: a stop must never reach the shared sandbox from this test.
    const stops: string[] = []
    await page.route('**/api/system/stop', (r) => {
      stops.push(r.request().method())
      return r.abort()
    })
    await page.goto('/admin/dashboard')
    const system = page.getByRole('region', { name: 'System' })
    await expect(system.getByRole('button', { name: 'Restart' })).toHaveCount(0)

    await system.getByRole('button', { name: 'Stop' }).click()
    const dialog = page.getByRole('dialog', { name: 'Stop jarvisd?' })
    await expect(dialog).toBeVisible()
    await expect(dialog).toContainText(info.stop.start_command)
    await expect(dialog.getByRole('button', { name: 'Stop jarvisd' })).toBeEnabled()
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)

    expect(stops).toEqual([])
    const after = await api('/api/system/info')
    expect(after.started_at).toBe(info.started_at)
  })

  test.describe('setup banners', () => {
    // setup.completed decides between them; each test sets it and puts the original back.
    let completed: unknown
    const setCompleted = (value: unknown) =>
      must('PUT', `${env('SBX_ADMIN')}/api/settings/admin/setup.completed`, { token: tokenOf('superuser'), body: { value } })
    test.beforeEach(async () => {
      const b = await api('/api/settings/?service=admin')
      completed = b.services[0].settings.find((x: { key: string }) => x.key === 'setup.completed')?.value ?? false
    })
    test.afterEach(() => setCompleted(completed))

    test('unfinished setup: the banner leads back to the wizard', async ({ page }) => {
      await setCompleted(false)
      expect((await api('/api/setup/state')).setup_step).toBeTruthy()
      await page.goto('/admin/dashboard')
      await expect(page.getByText("Setup isn't finished: models, privacy choices and next steps.")).toBeVisible()
      await expect(page.getByRole('link', { name: 'Finish setup' })).toHaveAttribute('href', '/setup')
      await expect(page.getByRole('status', { name: 'Voice setup' })).toHaveCount(0)
    })

    test('setup done but voice jobs missing: the banner names them and leads to their models', async ({ page }) => {
      await setCompleted(true)
      const setup = await api('/api/setup/state')
      expect(setup.setup_step ?? '').toBe('')
      const missing = Object.keys(REQUIRED).filter((id) => {
        const job = (setup.jobs ?? []).find((j: { job: string }) => j.job === id)
        return !UNDER_WAY.includes(job?.state ?? 'missing')
      })
      // The sandbox runs no speech models (nothing downloads), so stt and voice are missing.
      expect(missing.length).toBeGreaterThan(0)
      await page.goto('/admin/dashboard')
      const banner = page.getByRole('status', { name: 'Voice setup' })
      await expect(banner).toBeVisible()
      for (const id of missing) await expect(banner).toContainText(REQUIRED[id])
      await expect(page.getByRole('link', { name: 'Finish setup' })).toHaveCount(0)
      await banner.getByRole('link', { name: 'Set up models' }).click()
      await expect(page).toHaveURL(new RegExp(`/admin/models\\?job=${missing[0]}$`))
      await expect(page.getByText('Showing models for')).toContainText(REQUIRED[missing[0]])
    })
  })

  test('no update or GPU banner when the server reports neither', async ({ page }) => {
    const [update, labels] = await Promise.all([api('/api/update'), api('/api/llm/v1/models/labels')])
    // The sandbox has update checks off and no GPU; an "update available" state needs a fake
    // release server and jarvisd restarted onto it (scripts/upgrade-helper-e2e.sh), and a GPU
    // fault needs a GPU: both are out of this suite's reach, so only their absence is checked.
    expect(update.update_available).toBe(false)
    expect(labels.gpu_fault).toBeNull()
    await page.goto('/admin/dashboard')
    await expect(page.getByRole('region', { name: 'System' })).toBeVisible()
    await expect(page.getByText(/^jarvisd .* is available$/)).toHaveCount(0)
    await expect(page.getByRole('alert', { name: 'GPU problem' })).toHaveCount(0)
  })
})
