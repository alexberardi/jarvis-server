// Renders the mockups in directions.html to PNGs (every direction, page, viewport and theme) and
// the comparison page itself at phone and desktop width. Uses the Playwright that
// scripts/screenshots installs in tools/screenshots.
//
//   node docs/design/directions/render.mjs OUT_DIR
import { createRequire } from 'node:module'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const require = createRequire(path.join(here, '../../../tools/screenshots/package.json'))
const { chromium } = require('playwright')

const out = process.argv[2] ?? path.join(here, 'png')
fs.mkdirSync(out, { recursive: true })
const url = pathToFileURL(path.join(here, 'directions.html')).href
const SIZES = { desk: [1440, 900], phone: [390, 844] }

const browser = await chromium.launch()
for (const k of ['A', 'B', 'C']) {
  for (const pg of ['dash', 'chat']) {
    for (const [vp, [w, h]] of Object.entries(SIZES)) {
      for (const theme of ['light', 'dark']) {
        const page = await browser.newPage({ viewport: { width: w, height: h } })
        await page.goto(`${url}?frame=${k}-${pg}-${vp}&theme=${theme}`)
        await page.screenshot({ path: path.join(out, `${k}-${pg}-${vp}-${theme}.png`) })
        await page.close()
      }
    }
  }
}
for (const [vp, w] of [['phone', 390], ['desktop', 1440]]) {
  const page = await browser.newPage({ viewport: { width: w, height: 900 } })
  await page.goto(url)
  await page.waitForTimeout(200)
  await page.screenshot({ path: path.join(out, `comparison-${vp}.png`), fullPage: true })
  await page.close()
}
await browser.close()
console.log('rendered to', out)
