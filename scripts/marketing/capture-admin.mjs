#!/usr/bin/env node
// capture-admin.mjs: reproducible admin-UI screenshots for marketing (see README.md here).
//
// Signs in as a demo account, then captures a curated set of pages at desktop and phone sizes in
// the light and dark themes. It is read-only: every request the page makes other than a GET (or
// the token refresh) is aborted, so a stray click can't change the server. Volatile and private
// bits (host name, IP addresses, uptime, free disk, extra strings you name) are masked by script
// injected into the page, never by changing the app.
//
// Playwright is not a repo dependency. Install it anywhere and point PLAYWRIGHT_PREFIX at it:
//   npm i --prefix "$DIR" playwright && PLAYWRIGHT_BROWSERS_PATH="$DIR/browsers" \
//     npx --prefix "$DIR" playwright install chromium-headless-shell
//   PLAYWRIGHT_PREFIX="$DIR" PLAYWRIGHT_BROWSERS_PATH="$DIR/browsers" \
//     node scripts/marketing/capture-admin.mjs --out /tmp/shots

import { mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

const { values: args } = parseArgs({
  options: {
    base: { type: 'string', default: process.env.JARVIS_ADMIN_URL || 'http://localhost:7710' },
    email: { type: 'string', default: process.env.JARVIS_DEMO_EMAIL || 'test@jarvisautomation.io' },
    password: { type: 'string', default: process.env.JARVIS_DEMO_PASSWORD || 'DevPass123!' },
    out: { type: 'string' },
    only: { type: 'string' }, // comma-separated shot names
    themes: { type: 'string', default: 'light,dark' },
    viewports: { type: 'string', default: 'desktop,phone' },
    redact: { type: 'string', multiple: true, default: [] }, // extra private strings to cover
    'hide-room': { type: 'string', multiple: true, default: [] }, // drop these rooms' nodes' traces
    'host-label': { type: 'string', default: 'jarvis-home' },
    timeout: { type: 'string', default: '30000' },
    help: { type: 'boolean', short: 'h' },
  },
})

if (args.help || !args.out) {
  console.log(`usage: node capture-admin.mjs --out DIR [--base URL] [--only a,b] [--themes light,dark]
       [--viewports desktop,phone] [--redact STRING ...] [--host-label NAME] [--hide-room ROOM ...]
shots: dashboard models models-gpu nodes traces settings wizard-models`)
  process.exit(args.help ? 0 : 2)
}

const require = createRequire(join(resolve(process.env.PLAYWRIGHT_PREFIX || process.cwd()), 'noop.js'))
const { chromium } = require('playwright')

const VIEWPORTS = {
  desktop: { viewport: { width: 1440, height: 900 }, isMobile: false, hasTouch: false },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
}
const TIMEOUT = Number(args.timeout)

// Masking CSS: an opaque pill covers the value (a backdrop blur left small text readable).
// Animations and the caret are frozen so two runs give the same pixels.
const STYLE = `
  .mkt-cover-box { background: var(--color-surface-alt, #e2e8f0); box-shadow: inset 0 0 0 1px var(--color-border, #cbd5e1); border-radius: 4px; }
  *, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; }
  [data-sonner-toaster] { display: none !important; }
`

/**
 * maskPage runs in the page. The host name is swapped for hostLabel in place (a text change React
 * tolerates), so the UI still reads naturally. Everything else private or volatile is covered by
 * an opaque pill drawn over the matching text, without touching the app's DOM: IPv4
 * addresses, "up 3d 4h" uptimes, "456 GB free" disk numbers and any extra strings. Matching runs
 * on each element's joined text, because React splits "up {uptime}" into several text nodes.
 */
function maskPage({ hostname, hostLabel, extra, style }) {
  if (!document.getElementById('mkt-style')) {
    const s = document.createElement('style')
    s.id = 'mkt-style'
    s.textContent = style
    document.head.appendChild(s)
  }
  const esc = (x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  if (hostname) {
    const hostRe = new RegExp(esc(hostname), 'gi')
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (hostRe.test(n.nodeValue)) n.nodeValue = n.nodeValue.replace(hostRe, hostLabel)
      hostRe.lastIndex = 0
    }
  }
  const coverRe = new RegExp(
    [
      String.raw`\b(?:\d{1,3}\.){3}\d{1,3}\b`, // IPv4
      String.raw`\bup \d+[dhms](?: \d+[dhms])?`, // uptime
      String.raw`[\d.,]+\s?[KMGTP]?i?B free(?! of)`, // free disk ("9 GB free of 12 GB" is GPU memory)
      ...extra.filter(Boolean).map(esc),
    ].join('|'),
    'g',
  )
  document.querySelectorAll('.mkt-cover').forEach((el) => el.remove())
  const layer = document.createElement('div')
  layer.className = 'mkt-cover'
  layer.style.cssText = 'position:absolute;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none'
  document.body.appendChild(layer)
  // Leaf-ish elements: those with direct text children. Their joined text is matched, then each
  // match is mapped back onto the text nodes to measure it.
  for (const el of document.body.querySelectorAll('*')) {
    if (el.closest('.mkt-cover, script, style')) continue
    const texts = [...el.childNodes].filter((c) => c.nodeType === Node.TEXT_NODE)
    if (texts.length === 0) continue
    const joined = texts.map((t) => t.nodeValue).join('')
    for (const m of joined.matchAll(coverRe)) {
      let pos = 0
      const range = document.createRange()
      let started = false
      for (const t of texts) {
        const len = t.nodeValue.length
        if (!started && m.index < pos + len) {
          range.setStart(t, m.index - pos)
          started = true
        }
        if (started && m.index + m[0].length <= pos + len) {
          range.setEnd(t, m.index + m[0].length - pos)
          break
        }
        pos += len
      }
      if (!started) continue
      for (const r of range.getClientRects()) {
        if (r.width === 0) continue
        const box = document.createElement('div')
        box.className = 'mkt-cover-box'
        box.style.cssText = `position:absolute;left:${r.left + scrollX - 2}px;top:${r.top + scrollY - 1}px;width:${r.width + 4}px;height:${r.height + 2}px`
        layer.appendChild(box)
      }
    }
  }
  // Tooltips carry the data directory, listener ports and release strings; drop them all.
  for (const el of document.querySelectorAll('[title]')) el.removeAttribute('title')
}

// Waits shared by every page: the app shell is up and nothing is still loading.
async function settle(page, shot) {
  await page.locator(shot.root ?? 'h1').first().waitFor({ state: 'visible', timeout: TIMEOUT })
  await page.waitForFunction(
    () => !/^\s*(Loading|Running)(…|\.\.\.)\s*$/m.test(document.querySelector('main')?.innerText ?? document.body.innerText),
    null,
    { timeout: TIMEOUT },
  )
  await page.evaluate(() => document.fonts.ready)
}

/** The curated shots. Each opens a route and waits for the content that makes it worth showing. */
const SHOTS = {
  dashboard: {
    path: '/dashboard',
    async ready(page) {
      await page.getByText(/checks pass/).waitFor({ timeout: TIMEOUT })
      await page.getByText(/ of \d+ online/).waitFor({ timeout: TIMEOUT })
      await page.locator('section[aria-label="Recent requests"] li').first().waitFor({ timeout: TIMEOUT })
    },
  },
  models: {
    path: '/models',
    async ready(page) {
      await page.locator('section[aria-label="Jobs"]').waitFor({ timeout: TIMEOUT })
      // The live model is loaded on the GPU: the "private by default" proof.
      await page.getByText(/running/i).first().waitFor({ timeout: TIMEOUT })
    },
  },
  'models-gpu': {
    path: '/models',
    // The same page scrolled to what is loaded on the GPU.
    async ready(page) {
      const panel = page.locator('section[aria-label="Hardware and engines"]')
      await panel.getByText(/running/i).first().waitFor({ timeout: TIMEOUT })
      // Scroll <main> only; scrollIntoView would also shift the whole document.
      await panel.evaluate((el) => {
        const main = el.closest('main')
        main.scrollTop += el.getBoundingClientRect().top - main.getBoundingClientRect().top - 16
      })
    },
  },
  nodes: {
    path: '/nodes',
    async ready(page) {
      await page.getByText('ID:').first().waitFor({ timeout: TIMEOUT })
    },
  },
  traces: {
    path: '/traces',
    async ready(page) {
      // Expand the newest conversation that holds more than one request: a table row on a wide
      // screen (▶ in its first cell), a card button on a phone ("3 ▶").
      const wide = page.locator('table tbody tr:visible').filter({ has: page.locator('td:first-child', { hasText: '▶' }) })
      const cards = page.locator('[data-testid="trace-cards"] button[aria-expanded="false"]:visible').filter({ hasText: /(^|\D)([2-9]|\d{2,}) ▶/ }) // text runs together: "11.7s2 ▶"
      const target = wide.or(cards).first()
      await target.waitFor({ timeout: TIMEOUT })
      await target.click()
      await page.getByText('▼').filter({ visible: true }).first().waitFor({ timeout: TIMEOUT })
    },
  },
  settings: {
    path: '/settings',
    async ready(page) {
      await page.locator('main input, main select, main button[role="switch"]').first().waitFor({ timeout: TIMEOUT })
    },
  },
  'wizard-models': {
    path: '/setup',
    // Opening the wizard at a saved step writes nothing; only moving between steps does.
    session: { 'jarvis-admin:setup-step': 'llm' },
    root: '[aria-current="step"]', // the wizard has no h1
    async ready(page) {
      await page.locator('[aria-current="step"]').waitFor({ timeout: TIMEOUT })
      await page.getByText(/Language model/).first().waitFor({ timeout: TIMEOUT })
    },
  },
}

async function login(request) {
  const res = await request.post(`${args.base}/api/auth/login`, {
    data: { email: args.email, password: args.password },
  })
  if (!res.ok()) throw new Error(`login as ${args.email} failed: ${res.status()} ${await res.text()}`)
  return res.json()
}

async function main() {
  const out = resolve(args.out)
  mkdirSync(out, { recursive: true })
  const wanted = args.only ? args.only.split(',') : Object.keys(SHOTS)
  for (const w of wanted) if (!SHOTS[w]) throw new Error(`unknown shot ${w}`)

  const browser = await chromium.launch({ headless: true })
  try {
    const api = await browser.newContext()
    const tok = await login(api.request)
    const info = await (
      await api.request.get(`${args.base}/api/system/info`, { headers: { Authorization: `Bearer ${tok.access_token}` } })
    ).json()
    // Nodes in a hidden room (e.g. a real desktop agent) keep their traces out of the shots: the
    // trace list is filtered in the browser, the server is untouched.
    const hiddenNodes = new Set()
    if (args['hide-room'].length) {
      const live = await (
        await api.request.get(`${args.base}/api/cc/api/v0/admin/nodes`, { headers: { Authorization: `Bearer ${tok.access_token}` } })
      ).json()
      for (const n of live) if (args['hide-room'].includes(n.room)) hiddenNodes.add(n.node_id)
    }
    await api.close()
    const user = { ...tok.user, must_change_password: Boolean(tok.must_change_password ?? tok.user?.must_change_password) }
    const mask = { hostname: info.hostname || '', hostLabel: args['host-label'], extra: args.redact, style: STYLE }

    for (const vpName of args.viewports.split(',')) {
      for (const theme of args.themes.split(',')) {
        for (const name of wanted) {
          const shot = SHOTS[name]
          const ctx = await browser.newContext({
            ...VIEWPORTS[vpName],
            deviceScaleFactor: 2,
            colorScheme: theme,
            reducedMotion: 'reduce',
            timezoneId: 'America/New_York',
            locale: 'en-US',
          })
          await ctx.addInitScript(
            ({ tok, user, theme, session }) => {
              localStorage.setItem('jarvis-admin:access_token', tok.access_token)
              localStorage.setItem('jarvis-admin:refresh_token', tok.refresh_token)
              localStorage.setItem('jarvis-admin:user', JSON.stringify(user))
              localStorage.setItem('jarvis-admin-theme', theme)
              for (const [k, v] of Object.entries(session ?? {})) sessionStorage.setItem(k, v)
            },
            { tok, user, theme, session: shot.session },
          )
          // Read-only guard: only GETs reach the server, plus the token refresh.
          await ctx.route('**/*', (route) => {
            const req = route.request()
            const ok = req.method() === 'GET' || req.method() === 'HEAD' || /\/api\/auth\/refresh$/.test(new URL(req.url()).pathname)
            if (!ok) {
              console.warn(`  blocked ${req.method()} ${req.url()}`)
              return route.abort('blockedbyclient')
            }
            if (hiddenNodes.size && new URL(req.url()).pathname === '/api/traces') return filterTraces(route)
            return route.continue()
          })
          const filterTraces = async (route) => {
            const res = await route.fetch()
            const body = await res.json()
            const kept = (body.traces ?? []).filter((t) => !hiddenNodes.has(t.node_id))
            return route.fulfill({ response: res, json: { ...body, traces: kept, total: body.total - (body.traces.length - kept.length) } })
          }
          const page = await ctx.newPage()
          await page.goto(`${args.base}${shot.path}`, { waitUntil: 'domcontentloaded' })
          await settle(page, shot)
          await shot.ready(page)
          await page.evaluate(maskPage, mask)
          const file = join(out, `${name}-${vpName}-${theme}.png`)
          // The app scrolls inside <main>, so the shot is the fixed viewport.
          await page.screenshot({ path: file, animations: 'disabled', caret: 'hide' })
          console.log(`  ${file}`)
          await ctx.close()
        }
      }
    }
  } finally {
    await browser.close()
  }
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
