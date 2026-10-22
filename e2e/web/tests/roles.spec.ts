// What each role sees and can reach, from routes.tsx and Sidebar.tsx: every user has Chat,
// Add-ons and Inbox; the Admin group is the superuser's alone. A household admin, power user
// or member is not a superuser: the admin pages are hidden from them, a direct URL shows the
// administrator notice, and the server refuses the calls behind them (the gateway's tiers).
import { tokenOf } from '../helpers/api'
import { env, stateFile, type Role } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

const MEMBER_PAGES = [
  { label: 'Chat', path: '/chat' },
  { label: 'Add-ons', path: '/addons' },
  { label: 'Inbox', path: '/inbox' },
]

// label in the sidebar, path, the page's heading, and an API call the page makes.
const ADMIN_PAGES = [
  { label: 'Dashboard', path: '/admin/dashboard', heading: 'Dashboard', api: '/api/system/info' },
  { label: 'Models', path: '/admin/models', heading: 'Models', api: '/api/llm/v1/models/installed' },
  { label: 'Settings', path: '/admin/settings', heading: 'Settings', api: '/api/settings/' },
  { label: 'Connections', path: '/admin/connections', heading: 'Connections', api: '/api/connections' },
  { label: 'Logs', path: '/admin/logs', heading: 'Logs', api: '/api/logs?limit=1' },
  { label: 'Traces', path: '/admin/traces', heading: 'Traces', api: '/api/traces?limit=1' },
  { label: 'Nodes', path: '/admin/nodes', heading: 'Nodes', api: '/api/admin/nodes' },
  { label: 'Users', path: '/admin/users', heading: 'Users', api: '/api/admin/users' },
  { label: 'Phone', path: '/admin/phone', heading: 'Phone', api: '/api/phone/sip' },
  { label: 'Updates', path: '/admin/update', heading: 'Updates', api: '/api/update' },
]

const NOTICE = "This page is for the install's administrator"

test.describe('superuser', () => {
  test.use({ storageState: stateFile('superuser') })

  test('sees every page in the sidebar', async ({ page }) => {
    await page.goto('/chat')
    for (const p of [...MEMBER_PAGES, ...ADMIN_PAGES]) {
      await expect(sidebar(page).getByRole('link', { name: p.label, exact: true }), p.label).toHaveAttribute('href', p.path)
    }
  })

  for (const p of ADMIN_PAGES) {
    test(`opens ${p.path}`, async ({ page }) => {
      await page.goto(p.path)
      await expect(page.getByRole('heading', { name: p.heading, exact: true }).first()).toBeVisible()
      await expect(page.getByText(NOTICE)).toHaveCount(0)
    })
  }

  test('old top-level admin paths forward to /admin', async ({ page }) => {
    for (const p of [...ADMIN_PAGES.map((a) => a.path.replace('/admin', '')), '/traces/abc']) {
      await page.goto(p + '?q=1')
      await expect(page, p).toHaveURL(new RegExp(`/admin${p}\\?q=1$`))
    }
    await page.goto('/services')
    await expect(page).toHaveURL(/\/admin\/connections$/)
    await page.goto('/admin/services')
    await expect(page).toHaveURL(/\/admin\/connections$/)
    await page.goto('/admin')
    await expect(page).toHaveURL(/\/admin\/dashboard$/)
  })

  test('an unknown path shows the not-found page, in the shell and under /admin', async ({ page }) => {
    for (const p of ['/no-such-page', '/admin/no-such-page']) {
      await page.goto(p)
      await expect(page.getByRole('heading', { level: 1, name: 'Page not found' }), p).toBeVisible()
    }
    // Home is Chat, or the dashboard while the install needs attention (HomeRedirect).
    await page.getByRole('link', { name: 'Go home' }).click()
    await expect(page).toHaveURL(/\/(chat|admin\/dashboard)$/)
  })
})

for (const role of ['admin', 'power', 'member'] as Role[]) {
  test.describe(role, () => {
    test.use({ storageState: stateFile(role) })

    test('sees only Chat, Add-ons and Inbox', async ({ page }) => {
      await page.goto('/chat')
      const links = sidebar(page).getByRole('link')
      await expect(links).toHaveText(MEMBER_PAGES.map((p) => new RegExp(`^${p.label}`)))
      await expect(sidebar(page).getByText('Admin', { exact: true })).toHaveCount(0)
    })

    test('lands on Chat, not the dashboard', async ({ page }) => {
      await page.goto('/')
      await expect(page).toHaveURL(/\/chat$/)
    })

    test('a direct admin URL shows the notice, never the page', async ({ page }) => {
      for (const p of ADMIN_PAGES) {
        await page.goto(p.path)
        await expect(page.getByRole('heading', { name: NOTICE }), p.path).toBeVisible()
        await expect(page.getByRole('heading', { name: p.heading, exact: true })).toHaveCount(0)
      }
      // The old path forwards to the same notice.
      await page.goto('/users')
      await expect(page).toHaveURL(/\/admin\/users$/)
      await expect(page.getByRole('heading', { name: NOTICE })).toBeVisible()
      await page.getByRole('link', { name: 'Go to Chat' }).click()
      await expect(page).toHaveURL(/\/chat$/)
    })

    test('the server refuses the admin pages\' calls', async () => {
      const token = tokenOf(role)
      for (const p of ADMIN_PAGES) {
        const r = await fetch(env('SBX_ADMIN') + p.api, { headers: { Authorization: `Bearer ${token}` } })
        expect(r.status, `${p.api} as ${role}`).toBe(403)
      }
    })
  })
}

test('the superuser gets the same calls answered', async () => {
  const token = tokenOf('superuser')
  for (const p of ADMIN_PAGES) {
    const r = await fetch(env('SBX_ADMIN') + p.api, { headers: { Authorization: `Bearer ${token}` } })
    expect(r.status, p.api).toBe(200)
  }
})
