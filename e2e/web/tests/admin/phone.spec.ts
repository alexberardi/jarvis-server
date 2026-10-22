// /admin/phone: the SIP support page (docs/cc/phone-sip.md §6). The E2E sandboxes run Twilio,
// not SIP, so there is no SIP account here: the page says how to set one up, the self-test
// routes refuse an account that is not running, and the SIP trace still exports. The accounts,
// self-tests and a trace with real calls are covered on the L1 lane (tools/siplane, against
// Asterisk) and by the page's vitest tests.
import { readFileSync } from 'node:fs'
import { call, must, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

test.describe('admin phone', () => {
  test('with no SIP account: says how to set one up', async ({ page }) => {
    const status = await must('GET', `${env('SBX_ADMIN')}/api/phone/sip`, { token: tokenOf('superuser') })
    expect(status).toMatchObject({ accounts: [], tests: ['connection', 'echo', 'digits', 'call_me', 'inbound'], feature_codes: ['*43', '*65'] })
    await page.goto('/admin/phone')
    await expect(page.getByRole('heading', { level: 1, name: 'Phone' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'No SIP account is running' })).toBeVisible()
    await page.getByRole('link', { name: 'Open Settings' }).click()
    await expect(page).toHaveURL(/\/admin\/settings$/)
  })

  test('a self-test needs a running account', async () => {
    const r = await call('POST', `${env('SBX_ADMIN')}/api/phone/sip/5062/tests/echo`, { token: tokenOf('superuser'), body: {} })
    expect(r.status).toBe(404)
  })

  test('the SIP trace exports as a text file', async ({ page }) => {
    await page.goto('/admin/phone')
    const trace = page.getByRole('region', { name: 'SIP trace' })
    await expect(trace).toContainText('No calls traced since Jarvis started')
    const [download] = await Promise.all([page.waitForEvent('download'), trace.getByRole('button', { name: 'Export' }).click()])
    expect(download.suggestedFilename()).toMatch(/^jarvis-sip-trace-\d{8}-\d{6}\.txt$/)
    const text = readFileSync((await download.path())!, 'utf8')
    expect(text).toMatch(/^Jarvis SIP trace, /)
    expect(text).toContain('passwords and digest responses removed')
  })
})
