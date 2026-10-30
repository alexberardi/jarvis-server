// /admin/store (decision 2026-10-30-admin-store-page): the superuser browses its household's
// Pantry, keeps the default bundle and installs a package on a node it picks. The Pantry is a fake
// served by this spec, set for the household only; the test plays the node's side of the install
// (verify, then its result), so the page's progress comes from cc as it would from a real node.
import http from 'node:http'
import type { AddressInfo } from 'node:net'
import { call, householdOf, must, provisionNode, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const COMMIT = 'aab6498a5f6be699d39454d280625fa483c5ad42'
const PANTRY: Record<string, unknown> = {
  '/v1/commands': {
    commands: [{ command_name: 'e2e_hello', display_name: 'Hello', description: 'Says hello', author: 'e2e', latest_version: '1.2.0', categories: [], verified: true }],
    total: 1,
    page: 1,
    per_page: 100,
  },
  '/v1/commands/e2e_hello': { command_name: 'e2e_hello', display_name: 'Hello', description: 'Says hello', latest_version: '1.2.0', github_repo_url: 'https://github.com/e2e/hello', targets: [], apt_packages: [] },
  '/v1/commands/e2e_hello/download': { git_tag: COMMIT, git_commit_sha: COMMIT },
}

let pantry: http.Server
let hh: string
const settingsURL = () => `${env('SBX_CC')}/api/v0/mobile/household/${hh}/settings`

test.beforeAll(async () => {
  pantry = http.createServer((req, res) => {
    const body = PANTRY[new URL(req.url ?? '/', 'http://x').pathname]
    res.writeHead(body ? 200 : 404, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(body ?? { detail: 'Not found' }))
  })
  await new Promise<void>((ok) => pantry.listen(0, '127.0.0.1', ok))
  const token = tokenOf('superuser')
  hh = await householdOf(token)
  const url = `http://127.0.0.1:${(pantry.address() as AddressInfo).port}`
  await must('PUT', `${env('SBX_CC')}/settings/pantry.base_url?household_id=${hh}`, { token, body: { value: url } })
  await must('PUT', `${settingsURL()}/pantry.enabled`, { token, body: { value: true } })
})

test.afterAll(async () => {
  const token = tokenOf('superuser')
  await must('PUT', `${settingsURL()}/pantry.enabled`, { token, body: { value: false } })
  await must('PUT', `${settingsURL()}/pantry.default_bundle`, { token, body: { value: '' } })
  pantry.close()
})

test('a member is refused the Store\'s calls', async () => {
  for (const path of [`/api/cc/api/v0/households/${hh}/pantry/commands`, `/api/cc/api/v0/households/${hh}/pantry/commands/e2e_hello`]) {
    const r = await call('GET', env('SBX_ADMIN') + path, { token: tokenOf('member') })
    expect(r.status, path).toBe(403)
  }
})

test('keeps the default bundle', async ({ page }) => {
  await page.goto('/admin/store')
  await expect(page.getByRole('heading', { level: 1, name: 'Store' })).toBeVisible()
  await expect(page.getByTestId('package-e2e_hello')).toContainText('Says hello')
  await page.getByRole('switch', { name: 'Hello in the default bundle' }).click()
  await expect(page.getByRole('list', { name: 'In the bundle' })).toContainText('Hello')
  await expect.poll(async () => (await must('GET', settingsURL(), { token: tokenOf('superuser') })).settings['pantry.default_bundle']).toBe('e2e_hello')
})

test('installs a package on a chosen node', async ({ page }) => {
  const nodeId = crypto.randomUUID()
  const nodeKey = await provisionNode(tokenOf('superuser'), hh, { id: nodeId, name: 'store pi', room: 'store_room' })

  await page.goto('/admin/store')
  await page.getByRole('button', { name: 'Install Hello' }).click()
  const dialog = page.getByRole('dialog')
  // The superuser may be in more than one household: keep its own, then pick the node.
  if (await dialog.getByRole('button', { name: 'Next' }).isVisible()) await dialog.getByRole('button', { name: 'Next' }).click()
  await dialog.getByRole('checkbox', { name: /store pi/ }).check()
  const [created] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith(`/nodes/${nodeId}/package-install`) && r.request().method() === 'POST'),
    dialog.getByRole('button', { name: 'Install on 1 node' }).click(),
  ])
  expect(created.status()).toBe(201)
  expect(created.request().postDataJSON()).toEqual({ command_name: 'e2e_hello', github_repo_url: 'https://github.com/e2e/hello', git_tag: COMMIT })
  const { id } = await created.json()
  await expect(dialog.getByTestId('install-job')).toContainText('Installing')

  // The node's side: verify, then report success.
  const node = { 'X-API-Key': `${nodeId}:${nodeKey}` }
  const base = `${env('SBX_CC')}/api/v0/nodes/${nodeId}/package-install/${id}`
  await must('GET', `${base}/verify`, { headers: node })
  await must('POST', `${base}/results`, { headers: node, body: { success: true } })
  await expect(dialog.getByTestId('install-job')).toContainText('Installed')
})
