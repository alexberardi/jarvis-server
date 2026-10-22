// Screenshots of the admin SPA, for an agent (or a person) to look at. Driven by
// scripts/screenshots, which finds the sandbox and installs this directory's Playwright.
//
//   node screenshots.mjs --admin URL --email E --password P --out DIR
//                        [--pages a,b,/raw/path] [--viewports phone,tablet,desktop]
//                        [--themes light,dark] [--settle-ms N] [--tiles PX]
//
// Logs in through the real login form once, as whoever --email is, then for every viewport x
// theme opens a context with that session and visits each page. A superuser gets every page; a
// member (anyone else) gets their own pages, and an admin page they ask for shows the
// "administrator" notice, recorded as adminOnly rather than as an error. Per shot it records console errors, page errors,
// failed requests and HTTP errors, and measures horizontal overflow in the page's natural
// layout (before the shot is expanded to full height). Writes DIR/<page>__<viewport>__<theme>.png
// and DIR/index.json, and prints a summary.
import { chromium } from 'playwright'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { signIn } from './signin.mjs'

const VIEWPORTS = {
  phone: { width: 390, height: 844, isMobile: true, hasTouch: true },
  tablet: { width: 820, height: 1180, isMobile: true, hasTouch: true },
  desktop: { width: 1440, height: 900, isMobile: false, hasTouch: false },
}
const THEMES = ['light', 'dark']
const THEME_KEY = 'jarvis-admin-theme' // src/theme/ThemeProvider.tsx

// The Admin pages (web/admin/src/routes.tsx), superusers only. Known by name so a member can
// ask for one and see what they get: the notice (RequireSuperuser).
const ADMIN_PAGES = ['dashboard', 'models', 'settings', 'connections', 'logs', 'traces', 'nodes', 'users', 'phone', 'update']
  .map((n) => ({ name: n, path: `/admin/${n}` }))
// A member's default pages: the sidebar's, plus Account (the header's link).
const MEMBER_PAGES = ['chat', 'addons', 'inbox', 'workflows', 'recordings', 'account']
// The heading of the notice an admin page shows a member (auth/RequireSuperuser.tsx).
const ADMIN_ONLY = /administrator/i

// Pages that are not in the sidebar. Sidebar links are discovered after login.
//   public: visited without a session.  prepare: runs on the loaded page before the shot.
//   home: the path is the signed-in user's home (the dashboard, or Chat for a member).
const EXTRA_PAGES = [
  { name: 'account', path: '/account' },
  { name: 'login', path: '/login', public: true },
  { name: 'change-password', path: '/change-password' },
  // First-run setup as a signed-in user sees it: the wizard where the install left off, or a
  // redirect to the dashboard once setup is finished.
  { name: 'setup', path: '/setup' },
  { name: 'not-found', path: '/no-such-page' },
  // The phone/tablet navigation drawer, opened from the header's menu button.
  {
    name: 'nav-drawer',
    home: true,
    onlyIf: async (page) => page.getByRole('button', { name: 'Open navigation' }).isVisible(),
    prepare: async (page) => {
      await page.getByRole('button', { name: 'Open navigation' }).click()
      await page.waitForTimeout(400) // the slide-in transition
    },
    noExpand: true,
  },
]

function parseArgs(argv) {
  const a = { pages: '', viewports: 'phone,tablet,desktop', themes: 'light,dark', settleMs: 600, tiles: 0 }
  for (let i = 0; i < argv.length; i++) {
    const k = argv[i]
    const v = () => {
      if (i + 1 >= argv.length) throw new Error(`${k} needs a value`)
      return argv[++i]
    }
    switch (k) {
      case '--admin': a.admin = v().replace(/\/$/, ''); break
      case '--email': a.email = v(); break
      case '--password': a.password = v(); break
      case '--out': a.out = v(); break
      case '--pages': a.pages = v(); break
      case '--viewports': a.viewports = v(); break
      case '--themes': a.themes = v(); break
      case '--settle-ms': a.settleMs = Number(v()); break
      case '--tiles': a.tiles = Number(v()); break
      default: throw new Error(`unknown argument ${k}`)
    }
  }
  for (const k of ['admin', 'email', 'password', 'out']) if (!a[k]) throw new Error(`--${k} is required`)
  const list = (s) => s.split(',').map((x) => x.trim()).filter(Boolean)
  a.pages = list(a.pages)
  a.viewports = list(a.viewports)
  a.themes = list(a.themes)
  for (const v of a.viewports) if (!VIEWPORTS[v]) throw new Error(`unknown viewport ${v} (${Object.keys(VIEWPORTS)})`)
  for (const t of a.themes) if (!THEMES.includes(t)) throw new Error(`unknown theme ${t} (${THEMES})`)
  return a
}

const slug = (p) => p.replace(/^\/+|\/+$/g, '').replace(/[^a-zA-Z0-9]+/g, '-') || 'root'
// A sidebar link's page name: /admin/models is "models", /chat is "chat".
const pageName = (p) => slug(p.replace(/^\/admin\//, '/'))

async function login(browser, a) {
  const ctx = await browser.newContext({ viewport: VIEWPORTS.desktop })
  const page = await ctx.newPage()
  await signIn(page, a.admin, a.email, a.password)
  // The user the SPA stored at sign-in (auth/AuthContext.tsx): who the shots are of.
  const user = await page.evaluate(() => {
    try {
      return JSON.parse(localStorage.getItem('jarvis-admin:user') ?? 'null')
    } catch {
      return null
    }
  })
  const superuser = Boolean(user?.is_superuser)
  const home = superuser ? '/admin/dashboard' : '/chat'
  await page.goto(`${a.admin}${home}`)
  await page.locator('#admin-sidebar nav a[href]').first().waitFor({ state: 'attached', timeout: 15000 }) // Sidebar.tsx
  const nav = await page.$$eval('#admin-sidebar nav a[href]', (els) =>
    els.map((e) => new URL(e.href).pathname),
  )
  // A trace to show the detail page with, if the sandbox has any (superusers only).
  const traceId = !superuser ? null : await page.evaluate(async () => {
    const t = localStorage.getItem('jarvis-admin:access_token')
    const r = await fetch('/api/traces?limit=1', { headers: { Authorization: `Bearer ${t}` } })
    if (!r.ok) return null
    const d = await r.json()
    return d.traces?.[0]?.id ?? null
  })
  const state = await ctx.storageState()
  await ctx.close()
  return { state, nav: [...new Set(nav)], traceId, superuser, home, email: user?.email ?? a.email }
}

// choosePages: --pages, else everything this user has (a superuser: the sidebar, a trace, the
// extras; a member: MEMBER_PAGES). Admin pages are known by name for everyone, so a member
// asking for one gets its notice.
function choosePages(a, s) {
  const known = s.nav.map((p) => ({ name: pageName(p), path: p }))
  if (s.traceId) known.push({ name: 'trace-detail', path: `/admin/traces/${s.traceId}` })
  known.push(...EXTRA_PAGES.map((p) => (p.home ? { ...p, path: s.home } : p)))
  for (const p of ADMIN_PAGES) if (!known.some((k) => k.name === p.name)) known.push(p)
  const find = (want) => {
    const p = known.find((x) => x.name === want)
    if (!p) throw new Error(`unknown page ${want}; known: ${known.map((x) => x.name).join(', ')} (or a /path)`)
    return p
  }
  if (a.pages.length === 0) {
    if (!s.superuser) return MEMBER_PAGES.map(find)
    return known.filter((p) => s.nav.includes(p.path) || !ADMIN_PAGES.some((x) => x.name === p.name))
  }
  return a.pages.map((want) => (want.startsWith('/') ? { name: slug(want), path: want } : find(want)))
}

async function settle(page, ms) {
  await page.waitForLoadState('networkidle', { timeout: 5000 }).catch(() => {}) // streams never idle
  await page
    .waitForFunction(() => !document.querySelector('.animate-spin'), null, { timeout: 5000 })
    .catch(() => {})
  await page.waitForTimeout(ms)
}

// Runs in the page. The shell (AppShell) scrolls <main>, not the document, so overflow is
// measured against both. An offender is an element sticking out past the viewport that no inner
// scroll container (overflow-x not visible, and not <main> or one of its ancestors) clips; only
// the outermost offenders are reported, since their children stick out with them.
function measureOverflow(vw) {
  const main = document.querySelector('main')
  const se = document.scrollingElement
  const docOver = se.scrollWidth - vw
  const mainOver = main ? main.scrollWidth - main.clientWidth : 0
  const isPageScroller = (el) => el === se || el === document.body || (main && el.contains(main))
  const clips = (el) => {
    const ox = getComputedStyle(el).overflowX
    return ox !== 'visible' && !isPageScroller(el)
  }
  const describe = (el) => {
    let s = el.tagName.toLowerCase()
    if (el.id) s += `#${el.id}`
    const cls = typeof el.className === 'string' ? el.className.trim().split(/\s+/).slice(0, 4) : []
    if (cls.length && cls[0]) s += '.' + cls.join('.')
    const text = (el.innerText || el.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ').slice(0, 60)
    return text ? `${s} "${text}"` : s
  }
  const offenders = []
  const inner = []
  const truncated = []
  for (const el of document.body.querySelectorAll('*')) {
    const st = getComputedStyle(el)
    if (st.display === 'none' || st.visibility === 'hidden') continue
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    // Inner horizontal scrollers and ellipsis truncation: not page overflow, but worth a look
    // on a phone. (r.width > 2 skips sr-only labels, which are clipped to 1px on purpose.)
    if (clips(el) && el.scrollWidth > el.clientWidth + 1 && r.right <= vw + 1 && r.width > 2) {
      const entry = { el: describe(el), clientWidth: el.clientWidth, scrollWidth: el.scrollWidth }
      if (st.textOverflow === 'ellipsis') truncated.push(entry)
      else inner.push(entry)
    }
    if (r.right <= vw + 1 || r.left >= vw) continue
    let contained = false
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      if (clips(p)) { contained = true; break }
    }
    if (contained) continue
    if (offenders.some((o) => o.node.contains(el))) continue
    offenders.push({ node: el, el: describe(el), left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width) })
  }
  return {
    viewportWidth: vw,
    documentScrollWidth: se.scrollWidth,
    mainScrollWidth: main ? main.scrollWidth : null,
    mainClientWidth: main ? main.clientWidth : null,
    overflowPx: Math.max(docOver, mainOver, 0),
    offenders: offenders.slice(0, 12).map(({ node, ...o }) => o),
    innerScrollers: inner.slice(0, 12),
    truncated: truncated.slice(0, 12),
  }
}

// Runs in the page: let <main> and the shell grow to their content so a full-page shot shows
// the whole page rather than one screen of it.
function expandShell() {
  const main = document.querySelector('main')
  if (!main) return
  for (let el = main; el && el !== document.documentElement; el = el.parentElement) {
    el.style.setProperty('height', 'auto', 'important')
    el.style.setProperty('max-height', 'none', 'important')
    el.style.setProperty('overflow', 'visible', 'important')
    el.style.setProperty('min-height', '100dvh', 'important')
  }
  main.style.setProperty('min-height', '0', 'important')
}

async function shoot(ctx, pg, vpName, theme, a) {
  const page = await ctx.newPage()
  const consoleErrors = []
  const pageErrors = []
  const failedRequests = []
  const httpErrors = []
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 500)) })
  page.on('pageerror', (e) => pageErrors.push(String(e).slice(0, 500)))
  page.on('requestfailed', (r) => {
    const err = r.failure()?.errorText ?? ''
    if (err.includes('ERR_ABORTED')) return // streams and in-flight queries cut off by teardown
    failedRequests.push(`${r.method()} ${new URL(r.url()).pathname}: ${err}`)
  })
  page.on('response', (r) => {
    if (r.status() >= 400) httpErrors.push(`${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}`)
  })
  const rec = { page: pg.name, path: pg.path, viewport: vpName, theme }
  try {
    await page.goto(`${a.admin}${pg.path}`, { waitUntil: 'load' })
    await settle(page, a.settleMs)
    if (pg.onlyIf && !(await pg.onlyIf(page))) {
      await page.close()
      return null
    }
    if (pg.prepare) await pg.prepare(page)
    rec.finalPath = new URL(page.url()).pathname
    rec.title = await page.title()
    rec.heading = await page.locator('main h1, h1').first().textContent({ timeout: 1000 }).catch(() => null)
    // A member on an admin page: the notice is the expected page, not a failure.
    if (!a.superuser && rec.finalPath.startsWith('/admin') && ADMIN_ONLY.test(rec.heading ?? '')) rec.adminOnly = true
    rec.overflow = await page.evaluate(measureOverflow, VIEWPORTS[vpName].width)
    if (!pg.noExpand) await page.evaluate(expandShell)
    await page.waitForTimeout(100)
    rec.file = `${pg.name}__${vpName}__${theme}.png`
    await page.screenshot({ path: path.join(a.out, rec.file), fullPage: !pg.noExpand })
    // A tall full-page shot is shrunk to illegibility by image viewers (and by an agent's
    // image reader), so --tiles N also cuts it into N-px-high pieces: <file>.tNN.png.
    if (a.tiles > 0 && !pg.noExpand) {
      const { width } = VIEWPORTS[vpName]
      const height = await page.evaluate(() => document.documentElement.scrollHeight)
      rec.height = height
      if (height > a.tiles) {
        rec.tiles = []
        for (let y = 0, n = 1; y < height; y += a.tiles, n++) {
          const f = rec.file.replace(/\.png$/, `.t${String(n).padStart(2, '0')}.png`)
          const clip = { x: 0, y, width, height: Math.min(a.tiles, height - y) }
          await page.screenshot({ path: path.join(a.out, f), fullPage: true, clip })
          rec.tiles.push(f)
        }
      }
    }
  } catch (e) {
    rec.error = String(e).split('\n')[0]
  }
  Object.assign(rec, { consoleErrors, pageErrors, failedRequests, httpErrors })
  await page.close()
  return rec
}

async function main() {
  const a = parseArgs(process.argv.slice(2))
  await mkdir(a.out, { recursive: true })
  const browser = await chromium.launch()
  const shots = []
  let who = null
  try {
    const s = await login(browser, a)
    const { state } = s
    a.superuser = s.superuser
    who = { email: s.email, superuser: s.superuser }
    console.log(`signed in as ${s.email} (${s.superuser ? 'superuser' : 'not a superuser: member pages'})`)
    const pages = choosePages(a, s)
    for (const vpName of a.viewports) {
      for (const theme of a.themes) {
        const { width, height, isMobile, hasTouch } = VIEWPORTS[vpName]
        const mk = (storageState) =>
          browser.newContext({
            viewport: { width, height }, isMobile, hasTouch, deviceScaleFactor: 1,
            colorScheme: theme, storageState,
          })
        const authed = await mk(state)
        const anon = await mk(undefined)
        for (const c of [authed, anon]) {
          await c.addInitScript(([k, t]) => { try { localStorage.setItem(k, t) } catch { /* */ } }, [THEME_KEY, theme])
        }
        for (const pg of pages) {
          const rec = await shoot(pg.public ? anon : authed, pg, vpName, theme, a)
          if (!rec) continue
          shots.push(rec)
          const o = rec.overflow
          const flags = [
            rec.error && `ERROR ${rec.error}`,
            rec.adminOnly && 'administrator only (notice)',
            o && o.overflowPx > 0 && `overflow +${o.overflowPx}px`,
            rec.consoleErrors.length + rec.pageErrors.length && `${rec.consoleErrors.length + rec.pageErrors.length} console`,
            rec.failedRequests.length + rec.httpErrors.length && `${rec.failedRequests.length + rec.httpErrors.length} http`,
          ].filter(Boolean)
          console.log(`${rec.file ?? `${pg.name}__${vpName}__${theme}`}${flags.length ? '  ' + flags.join(', ') : ''}`)
        }
        await authed.close()
        await anon.close()
      }
    }
  } finally {
    await browser.close()
  }
  const index = { admin: a.admin, user: who, created: new Date().toISOString(), viewports: a.viewports, themes: a.themes, shots }
  await writeFile(path.join(a.out, 'index.json'), JSON.stringify(index, null, 2) + '\n')

  const problems = shots.filter((s) => s.error || s.overflow?.overflowPx > 0 || s.consoleErrors.length || s.pageErrors.length || s.failedRequests.length || s.httpErrors.length)
  console.log(`\n${shots.length} shots in ${a.out} (index.json); ${problems.length} with problems`)
  for (const s of shots.filter((s) => s.overflow?.overflowPx > 0 && s.theme === a.themes[0])) {
    console.log(`overflow ${s.page} @ ${s.viewport}: +${s.overflow.overflowPx}px`)
    for (const o of s.overflow.offenders.slice(0, 4)) console.log(`    ${o.el}  [${o.left}..${o.right}]`)
  }
  // Not page overflow, but what a phone user sees cut off or scrolling sideways inside the page.
  const narrow = a.viewports.reduce((m, v) => (VIEWPORTS[v].width < VIEWPORTS[m].width ? v : m))
  for (const s of shots.filter((s) => s.viewport === narrow && s.theme === a.themes[0] && s.overflow)) {
    for (const o of s.overflow.innerScrollers) console.log(`inner scroll ${s.page} @ ${narrow}: ${o.el} (${o.clientWidth}/${o.scrollWidth}px)`)
    for (const o of s.overflow.truncated) console.log(`truncated ${s.page} @ ${narrow}: ${o.el} (${o.clientWidth}/${o.scrollWidth}px)`)
  }
  if (shots.some((s) => s.error)) process.exitCode = 1
}

main().catch((e) => {
  console.error(`screenshots: ${e.message ?? e}`)
  process.exit(1)
})
