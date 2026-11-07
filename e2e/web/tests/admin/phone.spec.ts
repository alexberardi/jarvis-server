// /admin/phone: the SIP support page (docs/cc/phone-sip.md §6). The E2E sandboxes run Twilio,
// not SIP, so there is no SIP account here: the page says how to set one up, the self-test
// routes refuse an account that is not running, and the SIP trace still exports. The accounts,
// self-tests and a trace with real calls are covered on the L1 lane (tools/siplane, against
// Asterisk) and by the page's vitest tests. The phone settings on the page are edited for
// throwaway households, and the install's SIP account is put back to its defaults.
import { readFileSync } from 'node:fs'
import { call, must, newUser, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

interface Setting {
  key: string
  value: unknown
  from_db: boolean
  household_values?: { household_id: string; value: unknown }[]
}

async function setting(key: string): Promise<Setting> {
  const b = await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=cc`, { token: tokenOf('superuser') })
  const s = b.services.flatMap((svc: { settings: Setting[] }) => svc.settings).find((x: Setting) => x.key === key)
  if (!s) throw new Error(`no ${key} in /api/settings/`)
  return s
}

const ownValue = (s: Setting, hh: string) => s.household_values?.find((v) => v.household_id === hh)?.value

test.describe('admin phone', () => {
  test('with no SIP account: says how to set one up', async ({ page }) => {
    const status = await must('GET', `${env('SBX_ADMIN')}/api/phone/sip`, { token: tokenOf('superuser') })
    expect(status).toMatchObject({ accounts: [], tests: ['connection', 'echo', 'digits', 'call_me', 'inbound'], feature_codes: ['*43', '*65'] })
    await page.goto('/admin/phone')
    await expect(page.getByRole('heading', { level: 1, name: 'Phone' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'No SIP account is running' })).toBeVisible()
    await page.getByRole('link', { name: 'Set up the phone line' }).click()
    await expect(page).toHaveURL(/\/admin\/phone#phone-calls-section$/)
    await expect(page.getByRole('region', { name: 'Calls', exact: true })).toBeInViewport()
  })

  test('calls on for one household, then back to the default', async ({ page }) => {
    const key = 'phone_calls.enabled'
    const name = `Phone calls home ${Date.now().toString(36)}`
    const u = await newUser('phone-calls', name)
    const before = await setting(key)
    await page.goto('/admin/phone')
    const calls = page.getByRole('region', { name: 'Calls', exact: true })
    await calls.getByRole('button', { name: 'Set for a household', exact: true }).first().click()
    await calls.getByRole('combobox', { name: 'Household', exact: true }).selectOption(u.householdId)
    const editor = calls.getByRole('group', { name: `Set ${key} for a household` })
    await editor.getByRole('switch', { name: key, exact: true }).check()
    await editor.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText(`Updated for ${name}`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBe(true)
    if (before.value !== true) await expect(calls.getByRole('status')).toContainText(name)

    await calls.getByTestId(`household-value-${u.householdId}`).getByRole('button', { name: 'Use default', exact: true }).click()
    await expect(page.getByText(`${name} now uses the default`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBeUndefined()
  })

  test("an inbound value for one household: the answer delay", async ({ page }) => {
    const key = 'phone.inbound.answer_delay_seconds'
    const name = `Inbound home ${Date.now().toString(36)}`
    const u = await newUser('phone-inbound', name)
    await page.goto('/admin/phone')
    const inbound = page.getByRole('region', { name: 'Answering calls', exact: true })
    const row = inbound.getByTestId(`household-values-${key}`)
    await row.getByRole('button', { name: 'Set for a household', exact: true }).click()
    await row.getByRole('combobox', { name: 'Household', exact: true }).selectOption(u.householdId)
    await row.getByRole('spinbutton', { name: key, exact: true }).fill('5')
    await row.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText(`Updated for ${name}`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBe(5)

    // Out of range: the server's own validation message comes back.
    await row.getByTestId(`household-value-${u.householdId}`).getByRole('button', { name: `Edit ${name}`, exact: true }).click()
    await row.getByRole('spinbutton', { name: key, exact: true }).fill('99')
    await row.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText(/Failed to update: .*0 to 60/)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBe(5)
    await row.getByRole('button', { name: 'Cancel', exact: true }).click()

    await row.getByTestId(`household-value-${u.householdId}`).getByRole('button', { name: 'Use default', exact: true }).click()
    await expect(page.getByText(`${name} now uses the default`)).toBeVisible()
    expect(ownValue(await setting(key), u.householdId)).toBeUndefined()
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

  // Last: the install's SIP account is system-wide, so it is put back as found (the sandbox's
  // defaults) before the test ends. The PBX address does not resolve, so nothing registers.
  test("the install's SIP account: provider, server, extension and a write-only password", async ({ page }) => {
    const keys = ['phone.provider', 'phone.sip.server', 'phone.sip.extension', 'phone.sip.password', 'phone.sip.transport']
    const before = await Promise.all(keys.map((k) => setting(k)))
    for (const s of before) expect(s.from_db, `${s.key} starts at its default`).toBe(false)
    const secret = `e2e-not-a-secret-${Date.now().toString(36)}`
    try {
      await page.goto('/admin/phone')
      const calls = page.getByRole('region', { name: 'Calls', exact: true })
      await calls.getByRole('button', { name: 'Edit: Provider', exact: true }).click()
      await calls.getByRole('combobox', { name: 'Provider', exact: true }).selectOption('sip')
      await calls.getByRole('button', { name: 'Save', exact: true }).click()

      const line = page.getByRole('region', { name: 'Phone line: SIP', exact: true })
      const edit = async (label: string, value: string) => {
        await line.getByRole('button', { name: `Edit: ${label}`, exact: true }).click()
        await line.getByLabel(label, { exact: true }).fill(value)
        await line.getByRole('button', { name: 'Save', exact: true }).click()
        await expect(line.getByRole('button', { name: `Edit: ${label}`, exact: true })).toBeVisible()
      }
      await edit('PBX address', 'pbx.e2e.invalid')
      await edit('Extension', '7301')
      await edit('Extension password', secret)
      await line.getByRole('button', { name: 'Edit: Transport', exact: true }).click()
      await line.getByRole('combobox', { name: 'Transport', exact: true }).selectOption('tcp')
      await line.getByRole('button', { name: 'Save', exact: true }).click()

      await expect(line).toContainText('pbx.e2e.invalid')
      await expect(line).toContainText('7301')
      await expect(line).toContainText('set (hidden)')
      expect(await page.content()).not.toContain(secret)
      const after = Object.fromEntries((await Promise.all(keys.map((k) => setting(k)))).map((s) => [s.key, s.value]))
      expect(after).toEqual({
        'phone.provider': 'sip', 'phone.sip.server': 'pbx.e2e.invalid', 'phone.sip.extension': '7301',
        'phone.sip.password': '********', 'phone.sip.transport': 'tcp',
      })
    } finally {
      for (const k of keys) {
        await must('PUT', `${env('SBX_ADMIN')}/api/settings/cc/${k}`, { token: tokenOf('superuser'), body: { value: null } })
      }
    }
    for (const [i, k] of keys.entries()) expect((await setting(k)).value).toEqual(before[i].value)
  })
})
