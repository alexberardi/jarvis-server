// Called by TestLocalSettingsBrowser with its disposable server URL on stdin.
import { chromium, expect } from '../../e2e/web/node_modules/@playwright/test/index.mjs'
import { readFile, mkdir } from 'node:fs/promises'
let input = ''
for await (const chunk of process.stdin) input += chunk
const { url, settings, output } = JSON.parse(input)
const browser = await chromium.launch({ headless: true })
try {
 const page = await browser.newPage()
 const errors = []
 page.on('pageerror', e => errors.push(e.message))
 await page.goto(url)
 const form = page.locator('#settings-form')
 await expect(form.getByLabel('Follow-up duration')).toHaveValue('3')
 await form.getByLabel('Follow-up duration').fill('1.5')
 await form.getByLabel('Wake acknowledgment audio').uncheck()
 await form.getByLabel('On-screen indicator').uncheck()
 await form.getByRole('button', { name: 'Save changes' }).click()
 await expect(page.locator('#settings-result')).toContainText('Saved.')
 const saved = JSON.parse(await readFile(settings, 'utf8'))
 expect(saved.follow_up).toBe(1.5)
 expect(saved.chimes).toBe(false)
 expect(saved.island).toBe(false)
 await page.reload()
 await expect(form.getByLabel('Follow-up duration')).toHaveValue('1.5')
 await expect(form.getByLabel('Wake acknowledgment audio')).not.toBeChecked()
 await expect(form.getByLabel('On-screen indicator')).not.toBeChecked()
 await form.getByLabel('Speaker device').fill('wav:/tmp/private')
 await form.getByRole('button', { name: 'Save changes' }).click()
 await expect(page.locator('#settings-error')).toContainText('invalid audio device')
 await expect(form.getByLabel('Speaker device')).toHaveValue('wav:/tmp/private')
 await form.getByRole('button', { name: 'Discard changes and reload' }).click()
 await expect(form.getByLabel('Speaker device')).toHaveValue('default')
 for (const colorScheme of ['light', 'dark']) {
  for (const width of [390, 820, 1440]) {
   await page.emulateMedia({ colorScheme })
   await page.setViewportSize({ width, height: 900 })
   await page.locator('#settings-step').scrollIntoViewIfNeeded()
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
   if (output) {
    await mkdir(output, { recursive: true })
    await page.locator('#settings-step').screenshot({ path: `${output}/desktop-settings-${width}-${colorScheme}.png` })
   }
  }
 }
 expect(errors).toEqual([])
} finally { await browser.close() }
