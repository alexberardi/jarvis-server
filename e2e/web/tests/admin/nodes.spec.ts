// /admin/nodes: a computer asking to pair (device-code pairing) is approved here and joins, or
// is denied and told so; a signed-out node hides behind "Show signed-out nodes". The approval
// goes into a throwaway household of the superuser's, deleted afterwards with its nodes.
import type { Page } from '@playwright/test'
import { call, createHousehold, must, pollPairing, startPairing, tokenOf, unique } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const su = () => tokenOf('superuser')

async function typeCode(page: Page, code: string) {
  await page.goto('/admin/nodes')
  await expect(page.getByTestId('pairings-waiting')).toBeVisible()
  await page.getByPlaceholder('ABCD-EFGH').fill(code)
}

test.describe('admin nodes', () => {
  test('a pending computer is approved, joins the household, and once signed out hides behind the toggle', async ({ page }) => {
    const hhName = unique('Nodes home')
    const hh = await createHousehold(su(), hhName)
    try {
      const name = unique('Den PC')
      const room = unique('den')
      const p = await startPairing(name)
      expect((await pollPairing(p)).status).toBe('pending')

      await typeCode(page, p.code)
      const form = page.locator('form', { has: page.getByPlaceholder('ABCD-EFGH') })
      await form.getByLabel('Household').selectOption({ label: hhName })
      await form.getByLabel('Room').fill(room)
      await form.getByRole('button', { name: 'Approve' }).click()
      await expect(page.getByText(`Approved. The computer asks to confirm ${hhName} before it joins.`)).toBeVisible()

      // The computer's next poll gets its provisioning token, and it registers.
      const ok = await pollPairing(p)
      expect(ok).toMatchObject({ status: 'approved', household_id: hh, room })
      await must('POST', `${env('SBX_CC')}/api/v0/nodes/register`, {
        body: { node_id: ok.node_id, provisioning_token: ok.provisioning_token, room, node_kind: 'desktop' },
      })
      const listed = await must('GET', `${env('SBX_ADMIN')}/api/admin/nodes`, { token: su() })
      expect(listed.find((n: { node_id: string }) => n.node_id === ok.node_id)).toMatchObject({ household_id: hh, is_active: true })

      await page.reload()
      const card = page.getByTestId(`node-${ok.node_id}`)
      await expect(card).toContainText(name)
      await expect(card).toContainText('Active')
      await expect(card).toContainText(room)

      // Signed out (as Add-ons does), it leaves the list until asked for.
      await must('POST', `${env('SBX_ADMIN')}/api/cc/api/v0/nodes/${ok.node_id}/sign-out`, { token: su() })
      await page.reload()
      await expect(card).toHaveCount(0)
      await page.getByRole('button', { name: /^Show \d+ signed-out nodes?$/ }).click()
      await expect(card).toContainText('Inactive')
      await page.getByRole('button', { name: 'Hide signed-out nodes' }).click()
      await expect(card).toHaveCount(0)
    } finally {
      await call('DELETE', `${env('SBX_AUTH')}/households/${hh}`, { token: su() })
    }
  })

  test('a denied computer is told so and gets nothing', async ({ page }) => {
    const p = await startPairing(unique('Stranger PC'))
    await typeCode(page, p.code)
    await page.getByRole('button', { name: 'Deny' }).click()
    await expect(page.getByText('Pairing denied')).toBeVisible()
    await expect(page.getByPlaceholder('ABCD-EFGH')).toHaveValue('')
    const r = await pollPairing(p)
    expect(r).toEqual({ status: 'denied' })
    // The denial is final: the pairing is gone.
    const again = await call('POST', `${env('SBX_CC')}/api/v0/nodes/pairings/${p.pairing_id}/poll`, { body: { poll_secret: p.poll_secret } })
    expect(again.status).toBe(404)
  })

  test('renaming or removing a node', async () => {
    test.skip(true, 'the web admin has no rename or remove for a node (only sign-out from Add-ons); docs/decisions/2026-10-17-e2e-admin-pages.md')
  })
})
