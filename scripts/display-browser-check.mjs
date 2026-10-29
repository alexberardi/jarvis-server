// Render the installed display entry with a large, deterministic plugin catalogue.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from '../e2e/web/node_modules/playwright/index.mjs'
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const output = process.argv[2] || path.join(root, '.e2e/display')
await mkdir(output, { recursive: true })
const web = path.join(root, 'web/admin/dist/ui')
const server = createServer(async (req, res) => {
  try {
    const filename = path.resolve(web, `.${new URL(req.url, 'http://localhost').pathname}`)
    if (!filename.startsWith(`${web}${path.sep}`)) throw new Error('Invalid path')
    const body = await readFile(filename)
    res.setHeader('Content-Type', filename.endsWith('.js') ? 'application/javascript' : filename.endsWith('.css') ? 'text/css' : 'text/html')
    res.end(body)
  } catch { res.writeHead(404); res.end() }
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const base = `http://127.0.0.1:${server.address().port}`
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || '/usr/bin/chromium', headless: true })
const config = { waveform_colour: '#a56bff', waveform_emphasis: 150, enabled: true, clock: 'combined', hour_24: true, timezone: 'Australia/Perth', idle_seconds: 120, slide_seconds: 30, photos: [], favourites: ['d1'], night_dim: false, dim_start: 22, dim_end: 7, dim_percent: 35, destination: 'local', shortcuts: [{ name: 'Example streaming service', url: 'https://example.org' }] }
const devices = Array.from({ length: 1000 }, (_, i) => ({ id: `d${i}`, name: i === 0 ? 'Front door' : `Living room light ${i}`, entity_id: `light.${i}`, domain: i === 0 ? 'lock' : 'light', room_name: i % 2 ? 'Living room' : 'Kitchen', supported_actions: [{ button_action: i === 0 ? 'unlock' : 'turn_on', button_text: i === 0 ? 'Unlock' : 'Turn on' }] }))
const report = []
try {
  for (const viewport of [{ width: 1280, height: 800 }, { width: 800, height: 1280 }, { width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport, hasTouch: true, isMobile: true })
    const page = await context.newPage(), errors = [], actions = []
    let voice = 'idle', idle = false, clock = config.clock
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/api/**', async route => {
      const req = route.request(), api = new URL(req.url()).pathname
      if (api === '/api/voice-stream') {
        await route.fulfill({ contentType: 'text/event-stream', body: `data: ${JSON.stringify({ voice, level: 0.64 })}\n\n` })
        return
      }
      let body
      if (api === '/api/state') body = { configuration: { ...config, clock, idle_seconds: idle ? 0 : config.idle_seconds }, voice, last_interaction: idle ? Date.now() - 180000 : Date.now(), playback: { active: false, controllable: false }, theme_url: '', receivers: { playercast: 'running', cast: 'disabled' } }
      else if (api === '/api/devices') body = devices
      else if (api.endsWith('/control')) { actions.push(req.postDataJSON()); body = { success: true } }
      else if (api.endsWith('/state')) body = { state: { state: 'off' } }
      else body = { success: true }
      await route.fulfill({ json: body })
    })
    await page.goto(`${base}/display.html`)
    const mainClock = page.locator('.display-clock .digital')
    const fontSize = await mainClock.evaluate(n => parseFloat(getComputedStyle(n).fontSize))
    assert.ok(Math.abs(fontSize - Math.min(192, Math.max(78, viewport.width * 0.15))) < 1, 'Home clock should be 50% larger')
    await page.screenshot({ path: path.join(output, `Home-${viewport.width}x${viewport.height}.png`), fullPage: true })
    await page.getByRole('button', { name: 'Devices', exact: true }).tap()
    await page.getByRole('heading', { name: 'Front door', exact: true }).waitFor()
    assert.equal(await page.getByRole('article').count(), 24)
    await page.getByRole('button', { name: 'Unlock', exact: true }).tap()
    assert.equal(actions.length, 0)
    await page.getByRole('button', { name: 'Confirm', exact: true }).tap()
    await page.waitForTimeout(100)
    assert.equal(actions[0].confirmed, true)
    await page.getByLabel('Search devices').fill('light 999')
    assert.equal(await page.getByRole('article').count(), 1)
    await page.getByLabel('Search devices').fill('')
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    assert.equal(overflow, 0, 'horizontal overflow')
    const smallTargets = await page.locator('button:visible,input:visible,select:visible').evaluateAll(nodes => nodes.filter(n => n.getBoundingClientRect().height < 44).map(n => n.textContent))
    assert.deepEqual(smallTargets, [])
    const boundaryContrast = await page.getByLabel('Search devices').evaluate(node => {
      const style = getComputedStyle(node)
      const luminance = colour => {
        const c = colour.match(/[0-9.]+/g).slice(0, 3).map(Number).map(v => v / 255).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
        return c[0] * 0.2126 + c[1] * 0.7152 + c[2] * 0.0722
      }
      const a = luminance(style.borderTopColor), b = luminance(style.backgroundColor)
      return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
    })
    assert.ok(boundaryContrast >= 3, `input boundary contrast ${boundaryContrast} is below 3:1`)
    await page.screenshot({ path: path.join(output, `Devices-${viewport.width}x${viewport.height}.png`), fullPage: true })
    await page.getByRole('button', { name: 'Media', exact: true }).tap()
    await page.getByRole('heading', { name: 'Play something' }).waitFor()
    await page.screenshot({ path: path.join(output, `Media-${viewport.width}x${viewport.height}.png`), fullPage: true })
    voice = 'speaking'
    const waveform = page.getByLabel('Jarvis speaking', { exact: true })
    await waveform.waitFor({ state: 'visible' })
    const bounds = await waveform.boundingBox()
    assert.deepEqual(bounds, { x: 0, y: 0, width: viewport.width, height: viewport.height }, 'speech waveform must fill the display')
    const circle = await waveform.getByRole('img').boundingBox()
    assert.ok(Math.min(circle.width, circle.height) >= Math.min(viewport.width, viewport.height) * 0.8, 'circle should dominate the speech screen')
    const exit = page.getByRole('button', { name: 'Exit', exact: true })
    await exit.tap()
    await page.getByRole('heading', { name: 'Return to Xfce' }).waitFor()
    await page.getByRole('button', { name: 'Cancel', exact: true }).tap()
    await page.screenshot({ path: path.join(output, `Speech-${viewport.width}x${viewport.height}.png`), fullPage: true })
    voice = 'idle'
    await waveform.waitFor({ state: 'hidden' })
    idle = true
    for (const kind of ['digital', 'analogue', 'combined']) {
      clock = kind
      const idleClock = page.getByRole('button', { name: 'Wake Jarvis display' }).locator(`[data-clock="${kind}"]`)
      await idleClock.waitFor({ state: 'visible' })
      const clockBox = await idleClock.boundingBox()
      assert.ok(Math.abs(clockBox.width - viewport.width * 0.75) <= 1, 'idle clock should use 75% of screen width')
      assert.ok(Math.abs(clockBox.height - viewport.height * 0.75) <= 1, 'idle clock should use 75% of screen height')
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, 'idle clock must not overflow')
      await page.screenshot({ path: path.join(output, `Idle-${kind}-${viewport.width}x${viewport.height}.png`) })
    }
    assert.deepEqual(errors, [])
    report.push({ viewport, devices: devices.length, renderedDevices: 24, accessConfirmation: 'passed', fullScreenSpeech: 'passed', idleClock75Percent: 'passed', boundaryContrast, overflow, smallTargets, errors })
    await context.close()
  }
  await writeFile(path.join(output, 'Browser-Results.json'), JSON.stringify(report, null, 2))
  console.log(`Passed touch navigation, catalogue, confirmation and layout checks at ${report.length} sizes`)
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)) }
