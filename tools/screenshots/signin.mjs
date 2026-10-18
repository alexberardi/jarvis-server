// signIn logs in through the admin SPA's real login form, as a person would. Shared by
// screenshots.mjs and the E2E suite (e2e/web), so both follow the same form.
//
// A sign-in lands on /setup, which forwards to the dashboard once setup is finished and
// otherwise resumes the wizard (a fresh sandbox has not finished it); a member lands on Chat,
// and a temporary password on /change-password. Either way the session is in localStorage
// once the form is accepted, which is what this waits for. Throws with the page's alert text
// when the form is refused.

/** The SPA's session keys (src/auth/AuthContext.tsx). */
export const ACCESS_KEY = 'jarvis-admin:access_token'
export const USER_KEY = 'jarvis-admin:user'

/**
 * @param {import('playwright').Page} page
 * @param {string} admin the admin URL, no trailing slash
 * @param {string} email
 * @param {string} password
 * @param {{ timeout?: number }} [opts]
 */
export async function signIn(page, admin, email, password, opts = {}) {
  await page.goto(`${admin}/login`)
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  try {
    await page.waitForFunction((k) => localStorage.getItem(k), ACCESS_KEY, { timeout: opts.timeout ?? 15000 })
  } catch {
    const alert = await page.getByRole('alert').textContent({ timeout: 1000 }).catch(() => null)
    throw new Error(`login failed at ${page.url()}${alert ? `: ${alert}` : ''}`)
  }
}
