import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { once } from 'node:events'
import { householdOf, must, provisionNode, repoRoot, tokenOf, unique } from '../helpers/api'
import { env, stateFile } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import type { Page } from '@playwright/test'

test.use({ storageState: stateFile('member') })

async function captureDialog(page: Page, state: string) {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const [viewport, width, height] of [['phone', 390, 844], ['tablet', 820, 1180], ['desktop', 1440, 900]] as const) {
    await page.setViewportSize({ width, height })
    for (const theme of ['light', 'dark']) {
      await page.evaluate((dark) => document.documentElement.classList.toggle('dark', dark), theme === 'dark')
      // Theme colors use the shared 120ms transition; capture the settled palette.
      await page.waitForTimeout(200)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await page.screenshot({ path: test.info().outputPath(`${state}-${viewport}-${theme}.png`) })
    }
  }
  await page.evaluate(() => document.documentElement.classList.remove('dark'))
}

test('member discovers, names, pairs and removes a device through a desktop node', async ({ page, consoleErrors }) => {
  const token = tokenOf('member')
  const hh = await householdOf(token)
  const id = unique('device-node')
  const entity = `climate.${id}`
  const devicesURL = `${env('SBX_CC')}/api/v0/households/${hh}/devices`
  const key = await provisionNode(token, hh, { id, name: 'Devices test computer', room: 'Study', kind: 'desktop' })
  const scratch = mkdtempSync(path.join(tmpdir(), 'jarvis-device-node-'))
  const binary = path.join(scratch, 'device-node')
  let node: ReturnType<typeof spawn> | undefined
  try {
    // Use the repository toolchain fallback and shared build cache, with disposable output.
    execFileSync('bash', ['-c', '. scripts/lib/go.sh; CGO_ENABLED=0 go build -o "$1" ./e2e/web/fixtures/device-node', 'device-fixture', binary], { cwd: repoRoot })
    node = spawn(binary, [], { env: { ...process.env, DEVICE_NODE_CONFIG: JSON.stringify({ URL: env('SBX_CC'), Broker: env('SBX_MQTT'), ID: id, Key: key, Entity: entity }) } })
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('device fixture startup timed out')), 20_000)
      node!.once('error', (e) => { clearTimeout(timer); reject(e) })
      node!.once('exit', () => { clearTimeout(timer); reject(new Error('device fixture exited')) })
      node!.stdout!.once('data', () => { clearTimeout(timer); resolve() })
    })
    await page.goto('/devices')
    await expect(page.getByRole('heading', { name: 'Devices', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Add devices', exact: true }).click()
    await page.getByLabel('Scan using').selectOption(id)
    await page.getByRole('button', { name: 'Scan for devices' }).click()
    await page.getByRole('checkbox', { name: 'Select Test thermostat' }).check()
    await page.getByLabel('Name for Test thermostat').fill('Office heating')
    await captureDialog(page, 'discovery')
    await page.getByRole('button', { name: 'Add selected (1)' }).click()
    await expect(page.getByRole('button', { name: 'Manage Office heating' })).toBeVisible()
    const saved = (await must('GET', devicesURL, { token })).find((d: { entity_id: string }) => d.entity_id === entity)
    expect(saved.node_id).toBe(id)
    await page.getByRole('button', { name: 'Manage Office heating' }).click()
    await page.getByRole('button', { name: 'Pair device', exact: true }).click()
    await expect(page.getByLabel('Pairing code')).toBeFocused()
    await captureDialog(page, 'pairing')
    await page.getByLabel('Pairing code').fill('012-345')
    await page.getByRole('button', { name: 'Submit code' }).click()
    await expect(page.getByText('Pairing completed.', { exact: true })).toBeVisible()
    await page.getByLabel('Device name').fill('Study heating')
    await page.getByRole('button', { name: 'Save changes' }).click()
    await page.reload()
    await page.getByRole('button', { name: 'Manage Study heating' }).click()
    await page.getByRole('button', { name: 'Remove device', exact: true }).click()
    await page.getByRole('button', { name: 'Remove from Jarvis' }).click()
    await expect(page.getByRole('button', { name: 'Manage Study heating' })).toHaveCount(0)
    expect((await must('GET', devicesURL, { token })).some((d: { id: string }) => d.id === saved.id)).toBe(false)
    expect(consoleErrors).toEqual([])
  } finally {
    if (node && node.exitCode === null && node.signalCode === null) { const exited = once(node, 'exit'); node.kill('SIGTERM'); await exited }
    rmSync(scratch, { recursive: true, force: true })
    const leftover = (await must('GET', devicesURL, { token })).find((d: { entity_id: string }) => d.entity_id === entity)
    if (leftover) await must('DELETE', `${devicesURL}/${leftover.id}`, { token })
    await must('DELETE', `${env('SBX_CC')}/api/v0/admin/nodes/${id}`, { token: tokenOf('superuser') })
  }
})
