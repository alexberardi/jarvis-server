// The inbox: what a service posts shows up, opening an item reads it, and the actions change
// the items on the server.
import { call, createInboxItem, inbox, newUser, uploadAttachment } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, sidebar, test } from '../helpers/fixtures'

test('lists items with the unread badge, and the detail marks one read', async ({ asUser }) => {
  const u = await newUser('inbox')
  const a = await createInboxItem(u, { title: 'Research: heat pumps', body: '## Findings\n\nHeat pumps **work** in cold climates.' })
  await createInboxItem(u, { title: 'Research: solar', body: 'Panels.' })

  const page = await asUser(u)
  await page.goto('/inbox')
  await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()
  const list = page.locator('main li')
  await expect(list).toHaveCount(2)
  await expect(page.locator('main').getByLabel('Unread', { exact: true })).toHaveCount(2)
  await expect(sidebar(page).getByLabel('2 unread')).toBeVisible()

  await page.getByRole('link', { name: /Research: heat pumps/ }).click()
  await expect(page).toHaveURL(new RegExp(`/inbox/${a.id}$`))
  await expect(page.getByRole('heading', { name: 'Research: heat pumps' })).toBeVisible()
  // The body is rendered as markdown, not shown as source.
  await expect(page.getByRole('heading', { name: 'Findings' })).toBeVisible()
  await expect(page.locator('strong', { hasText: 'work' })).toBeVisible()

  await expect.poll(async () => (await inbox(u.token)).find((i) => i.id === a.id)?.is_read).toBe(true)
  await page.getByRole('link', { name: 'Inbox' }).first().click()
  await expect(page.locator('main').getByLabel('Unread', { exact: true })).toHaveCount(1)
  await expect(sidebar(page).getByLabel('1 unread')).toBeVisible()
})

test('deleting from the detail page removes the item', async ({ asUser }) => {
  const u = await newUser('inboxdel')
  const a = await createInboxItem(u, { title: 'Delete me', body: 'Soon gone.' })
  const page = await asUser(u)
  await page.goto(`/inbox/${a.id}`)
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Delete' }).click()
  await expect(page).toHaveURL(/\/inbox$/)
  await expect(page.getByText('Nothing here yet')).toBeVisible()
  expect(await inbox(u.token)).toEqual([])

  // A gone item's URL says so instead of a blank page.
  await page.goto(`/inbox/${a.id}`)
  await expect(page.getByRole('alert')).toBeVisible()
})

test('select: mark read and delete several at once', async ({ asUser }) => {
  const u = await newUser('inboxbulk')
  const keep = await createInboxItem(u, { title: 'Keep this one' })
  const x = await createInboxItem(u, { title: 'Bulk one' })
  const y = await createInboxItem(u, { title: 'Bulk two' })
  const page = await asUser(u)
  await page.goto('/inbox')
  await expect(page.locator('main li')).toHaveCount(3)

  await page.getByRole('button', { name: 'Select' }).click()
  await page.getByLabel('Select Bulk one').check()
  await page.getByLabel('Select Bulk two').check()
  await page.getByRole('button', { name: 'Mark read' }).click()
  await expect
    .poll(async () => (await inbox(u.token)).filter((i) => i.is_read).map((i) => i.id).sort())
    .toEqual([x.id, y.id].sort())

  // The action ends selection mode.
  await page.getByRole('button', { name: 'Select' }).click()
  await page.getByLabel('Select Bulk one').check()
  await page.getByLabel('Select Bulk two').check()
  await page.getByRole('button', { name: 'Delete' }).click()
  await expect.poll(async () => (await inbox(u.token)).map((i) => i.id)).toEqual([keep.id])
  await expect(page.locator('main li')).toHaveCount(1)
  await expect(page.locator('main li')).toContainText('Keep this one')
})

// A 1×1 PNG, and enough of an MP4 (an ftyp box) for the server's sniffing.
const png = Uint8Array.from(
  atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=='),
  (c) => c.charCodeAt(0),
)
const mp4 = new Uint8Array([...new TextEncoder().encode('\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2'), ...new Uint8Array(64)])

test("the detail shows an item's image and video attachments, only to its user", async ({ asUser }) => {
  const u = await newUser('inboxatt')
  const img = await uploadAttachment(png, 'image/png')
  const vid = await uploadAttachment(mp4, 'video/mp4')
  const a = await createInboxItem(u, { title: 'Made for you', body: 'Two files.', attachment_ids: [img.id, vid.id] })

  const page = await asUser(u)
  await page.goto(`/inbox/${a.id}`)
  const shown = page.getByRole('region', { name: 'Attachments' })
  const image = shown.getByRole('img', { name: 'Attachment 1' })
  await expect(image).toHaveAttribute('src', /^blob:/)
  // The bytes decoded: a broken image has no natural size.
  await expect.poll(() => image.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBe(1)
  const video = shown.getByLabel('Attachment 2')
  await expect(video).toHaveAttribute('src', /^blob:/)
  await expect(video).toHaveAttribute('controls', '')

  // Someone else (another household) gets a 404 for the file, not the bytes.
  const other = await newUser('inboxatt2')
  const url = `${env('SBX_NOTIFICATIONS')}/api/v0/inbox/${a.id}/attachments/${img.id}`
  expect((await call('GET', url, { token: other.token })).status).toBe(404)
  expect((await call('GET', url, { token: u.token })).status).toBe(200)
})
